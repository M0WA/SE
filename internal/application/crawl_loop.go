package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// crawlLoop drives the crawl control flow shared by every CrawlerService
// backend: seed queue, dedup, robots check, fetch, and thin-content
// filtering. save is called once per successfully fetched, non-thin page;
// what it does with that page (in-memory index vs. SQL + embedding) is the
// one thing that actually differs between backends.
func crawlLoop(
	ctx context.Context,
	fetcher ports.AuthFetcher,
	robots ports.RobotsChecker,
	parseHTML func(html, pageURL string) (title, text string, links []string),
	settings *domain.OperationalSettings,
	opts ports.CrawlOptions,
	isIndexed func(url string) bool,
	save func(ctx context.Context, doc domain.Document) error,
	onPage func(domain.CrawlPageEvent),
) (int, error) {
	v := settings.Get()
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = v.DefaultMaxPages
	}
	minTextLength := v.MinTextLength
	if opts.MinTextLength > 0 {
		minTextLength = opts.MinTextLength
	}
	crawlDelayMs := v.CrawlDelayMs
	if opts.CrawlDelayMs > 0 {
		crawlDelayMs = opts.CrawlDelayMs
	}
	maxResponseBytes := 0
	if opts.MaxResponseKB > 0 {
		maxResponseBytes = opts.MaxResponseKB * 1024
	}
	fetchOpts := ports.FetchOptions{
		Cookie: opts.Cookie, BasicAuthUser: opts.BasicAuthUser, BasicAuthPass: opts.BasicAuthPass,
		UserAgent: opts.UserAgent, FetchTimeoutSeconds: opts.FetchTimeoutSeconds, MaxResponseBytes: maxResponseBytes,
		Renderer: opts.Renderer,
	}
	// A sitemap is XML, never a page to render -- NoRender forces the
	// plain HTTP path regardless of this crawl's own Renderer or the
	// Tuning page's global default, so a rendering-aware fetcher (see
	// application.RenderAwareFetcher) never hands it to a real browser.
	sitemapFetchOpts := fetchOpts
	sitemapFetchOpts.NoRender = true

	emit := func(ev domain.CrawlPageEvent) {
		if onPage != nil {
			onPage(ev)
		}
	}

	linkScope := opts.LinkScope
	if linkScope == domain.LinkScopeDefault {
		linkScope = v.LinkScope
	}
	scope := newLinkScopeMatcher(opts.SeedURLs)

	// prioritize is true only when the caller both asked for it
	// (opts.PrioritizeUnindexed) and can actually tell fresh from
	// already-indexed URLs (isIndexed != nil, backed by a real repository
	// lookup -- see sqlCrawlerService.Crawl). freshQueue/knownQueue split
	// discovery in two: fresh URLs are always dequeued first, known ones
	// only once no fresh URL is left, so a bounded maxPages budget spends
	// itself on new content before refreshing what's already indexed.
	// When prioritize is false, every URL lands in freshQueue and this is
	// byte-for-byte the single FIFO queue this crawl loop always had.
	prioritize := opts.PrioritizeUnindexed && isIndexed != nil
	var freshQueue, knownQueue []string
	classify := func(urls []string) {
		for _, u := range urls {
			if prioritize && isIndexed(u) {
				knownQueue = append(knownQueue, u)
			} else {
				freshQueue = append(freshQueue, u)
			}
		}
	}
	dequeue := func() (string, bool) {
		if len(freshQueue) > 0 {
			u := freshQueue[0]
			freshQueue = freshQueue[1:]
			return u, true
		}
		if len(knownQueue) > 0 {
			u := knownQueue[0]
			knownQueue = knownQueue[1:]
			return u, true
		}
		return "", false
	}
	enqueue := func(links []string) {
		var allowed []string
		for _, l := range links {
			if scope.allows(l, linkScope) {
				allowed = append(allowed, l)
			}
		}
		classify(allowed)
	}

	visited := make(map[string]bool)
	classify(opts.SeedURLs)

	if opts.UseSitemap {
		for _, seed := range opts.SeedURLs {
			sitemap, ok := sitemapURL(seed)
			if !ok {
				continue
			}
			body, err := fetcher.FetchWithOptions(ctx, sitemap, sitemapFetchOpts)
			if err != nil {
				continue
			}
			enqueue(parseSitemap(body))
		}
	}

	crawled := 0
	fetchCount := 0

	for crawled < maxPages {
		if err := ctx.Err(); err != nil {
			return crawled, err
		}

		u, ok := dequeue()
		if !ok {
			break
		}

		if visited[u] || !isHTTP(u) {
			continue
		}
		visited[u] = true
		attemptedAt := time.Now()

		if opts.RespectRobots && robots != nil && !robots.Allowed(ctx, u) {
			emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageRobotsDisallowed, FetchedAt: attemptedAt})
			continue
		}

		if fetchCount > 0 && crawlDelayMs > 0 {
			select {
			case <-time.After(time.Duration(crawlDelayMs) * time.Millisecond):
			case <-ctx.Done():
				return crawled, ctx.Err()
			}
		}
		fetchCount++

		fetchStart := time.Now()
		html, err := fetcher.FetchWithOptions(ctx, u, fetchOpts)
		durationMs := time.Since(fetchStart).Milliseconds()
		if err != nil {
			emit(domain.CrawlPageEvent{
				URL: u, Status: domain.CrawlPageFetchFailed, Error: err.Error(),
				FetchedAt: attemptedAt, DurationMs: durationMs,
			})
			continue
		}

		title, text, links := parseHTML(html, u)
		trimmed := strings.TrimSpace(text)
		if len(trimmed) < minTextLength {
			emit(domain.CrawlPageEvent{
				URL: u, Status: domain.CrawlPageThinContent,
				Error:     fmt.Sprintf("%d characters, need at least %d", len(trimmed), minTextLength),
				DocLength: len(trimmed),
				FetchedAt: attemptedAt, DurationMs: durationMs,
			})
			continue
		}

		doc := domain.Document{ID: documentID(u), URL: u, Title: title, Text: text, Links: links}
		if err := save(ctx, doc); err != nil {
			return crawled, err
		}
		crawled++
		emit(domain.CrawlPageEvent{
			URL: u, Status: domain.CrawlPageIndexed, Title: title,
			DocLength: len(trimmed), LinksFound: len(links),
			FetchedAt: attemptedAt, DurationMs: durationMs,
		})

		enqueue(links)
	}
	return crawled, nil
}

// linkScopeMatcher decides whether a discovered link is within scope for a
// crawl, relative to its seed URL(s) -- see domain.LinkScope*. hosts holds
// every seed's exact host (for domain.LinkScopeHost); domains holds every
// seed host's registrable domain, i.e. its effective-TLD-plus-one (for
// domain.LinkScopeDomain); names holds every seed host's domain name with
// its public suffix stripped off (for domain.LinkScopeTLD), so matching
// ignores which TLD a link uses.
type linkScopeMatcher struct {
	hosts   map[string]bool
	domains map[string]bool
	names   map[string]bool
}

func newLinkScopeMatcher(seeds []string) linkScopeMatcher {
	m := linkScopeMatcher{
		hosts:   make(map[string]bool, len(seeds)),
		domains: make(map[string]bool, len(seeds)),
		names:   make(map[string]bool, len(seeds)),
	}
	for _, s := range seeds {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Host)
		m.hosts[host] = true
		m.domains[registrableDomain(host)] = true
		m.names[domainName(host)] = true
	}
	return m
}

// allows reports whether rawURL is within scope, given the crawl's already-
// resolved LinkScope (opts.LinkScope, or the Tuning page's global default
// when that was left blank -- see crawlLoop). domain.LinkScopeAny always
// allows; domain.LinkScopeHost requires an exact match against a seed's own
// host; domain.LinkScopeDomain (the default) allows any host that shares a
// seed's registrable domain -- covering that seed's own host, any of its
// subdomains, and its bare registrable domain; domain.LinkScopeTLD allows
// any host whose domain name matches a seed's, regardless of subdomain or
// which TLD it uses (e.g. a seed of example.com also allows example.org and
// www.example.co.uk, but not other.com).
func (m linkScopeMatcher) allows(rawURL, scope string) bool {
	if scope == domain.LinkScopeAny {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	switch scope {
	case domain.LinkScopeHost:
		return m.hosts[host]
	case domain.LinkScopeTLD:
		return m.names[domainName(host)]
	default:
		return m.domains[registrableDomain(host)]
	}
}

// registrableDomain returns host's effective TLD plus one label (e.g.
// "blog.example.co.uk" -> "example.co.uk"), using the public suffix list so
// multi-part TLDs (".co.uk", ".com.au", ...) are handled correctly rather
// than naively taking "the last two labels". Falls back to host itself for
// anything the list can't derive an eTLD+1 for (a bare IP address,
// "localhost", or a host that's already a public suffix on its own) -- such
// a host still only ever matches itself, never anything else.
func registrableDomain(host string) string {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etld1
}

// domainName returns host's registrable domain with its public suffix
// stripped off (e.g. "blog.example.co.uk" -> "example"), for
// domain.LinkScopeTLD: comparing this ignores both subdomains and which TLD
// a link uses. Falls back to registrableDomain's own fallback (host itself)
// for anything PublicSuffix can't strip a recognized suffix from (a bare IP
// address, "localhost", or an already-bare public suffix), so such a host
// still only ever matches itself.
func domainName(host string) string {
	reg := registrableDomain(host)
	if suffix, _ := publicsuffix.PublicSuffix(host); suffix != "" {
		if name, ok := strings.CutSuffix(reg, "."+suffix); ok {
			return name
		}
	}
	return reg
}

// sitemapURL builds the /sitemap.xml URL at a seed's origin, discarding any
// path/query/fragment the seed itself carried.
func sitemapURL(seed string) (string, bool) {
	u, err := url.Parse(seed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	u.Path = "/sitemap.xml"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

// sitemapURLSet is the small slice of the sitemaps.org urlset schema this
// crawler actually uses -- just each entry's <loc>.
type sitemapURLSet struct {
	XMLName xml.Name `xml:"urlset"`
	Entries []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

// parseSitemap extracts every <loc> from a sitemap.xml body. A body that
// isn't valid XML, or isn't a urlset, just yields no URLs -- the caller
// treats a sitemap as an optional bonus, never a reason to fail the crawl.
func parseSitemap(body string) []string {
	var set sitemapURLSet
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		return nil
	}
	urls := make([]string, 0, len(set.Entries))
	for _, e := range set.Entries {
		if loc := strings.TrimSpace(e.Loc); loc != "" {
			urls = append(urls, loc)
		}
	}
	return urls
}

func isHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}

// documentID derives a stable ID from a URL, so re-crawling the same page
// always upserts the same row instead of creating a duplicate under a new
// ID -- the SQL repository's uniqueness/versioning relies on this.
func documentID(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return "doc-" + hex.EncodeToString(sum[:8])
}
