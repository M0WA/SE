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
// backend: seed queue, dedup, robots check, fetch, thin-content filtering.
// save runs once per fetched, non-thin page; what it does with that page
// is the only thing that differs between backends.
func crawlLoop(
	ctx context.Context,
	fetcher ports.AuthFetcher,
	robots ports.RobotsChecker,
	parseHTML func(html, pageURL string) (title, text string, links []string, canonicalURL string),
	settings *domain.OperationalSettings,
	opts ports.CrawlOptions,
	isIndexed func(url string) bool,
	// isDomainIndexed batch-checks whether a host already has an indexed
	// document, for opts.FollowIndexedDomains -- nil when off.
	isDomainIndexed func(hosts []string) map[string]bool,
	save func(ctx context.Context, doc domain.Document) error,
	// recordAlias persists that aliasURL's content lives under canonicalID,
	// called instead of save when a canonical link resolves elsewhere.
	// nil-safe: without it, an aliased page is saved as its own document.
	recordAlias func(ctx context.Context, aliasURL, canonicalID string) error,
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
	// plain HTTP path regardless of this crawl's Renderer setting.
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
	allowlist := newDomainListMatcher(opts.AllowedDomains)
	blocklist := newDomainListMatcher(opts.BlockedDomains)

	// prioritize is true only when PrioritizeUnindexed is set and isIndexed
	// is available. freshQueue/knownQueue split lets fresh URLs dequeue
	// first, spending the maxPages budget on new content before refreshing
	// what's indexed. When false, everything lands in freshQueue.
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
	popFront := func(q *[]string) (string, bool) {
		if len(*q) == 0 {
			return "", false
		}
		u := (*q)[0]
		*q = (*q)[1:]
		return u, true
	}
	dequeue := func() (string, bool) {
		if u, ok := popFront(&freshQueue); ok {
			return u, true
		}
		return popFront(&knownQueue)
	}
	// enqueue precedence: BlockedDomains always rejects; then LinkScope or
	// AllowedDomains allows; finally isDomainIndexed (if available) allows
	// a link whose host already has an indexed document -- widens scope,
	// never narrows it. Called once per batch of distinct hosts.
	enqueue := func(links []string) {
		var allowed []string
		var undecided []string
		for _, l := range links {
			host := domain.HostOf(l)
			if host == "" || blocklist.matches(host) {
				continue
			}
			if allowlist.matches(host) || scope.allows(l, linkScope) {
				allowed = append(allowed, l)
				continue
			}
			if isDomainIndexed != nil {
				undecided = append(undecided, l)
			}
		}
		if len(undecided) > 0 {
			hostSeen := make(map[string]bool, len(undecided))
			hosts := make([]string, 0, len(undecided))
			for _, l := range undecided {
				h := domain.HostOf(l)
				if !hostSeen[h] {
					hostSeen[h] = true
					hosts = append(hosts, h)
				}
			}
			indexed := isDomainIndexed(hosts)
			for _, l := range undecided {
				if indexed[domain.HostOf(l)] {
					allowed = append(allowed, l)
				}
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

		title, text, links, canonicalURL := parseHTML(html, u)
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

		selfURL := domain.CanonicalizeURL(u, v.URLAliasWWWEnabled)
		// A <link rel="canonical"> naming a different URL means this page's
		// content indexes only under that URL -- no document row for u. Its
		// links are enqueued as usual, plus the canonical target itself:
		// otherwise real sites often never link to their own canonical URL
		// (e.g. one stripped of tracking params), leaving the alias
		// dangling forever.
		if recordAlias != nil && canonicalURL != "" {
			if canon := domain.CanonicalizeURL(canonicalURL, v.URLAliasWWWEnabled); canon != selfURL {
				_ = recordAlias(ctx, selfURL, documentID(canon, v.URLAliasWWWEnabled))
				emit(domain.CrawlPageEvent{
					URL: u, Status: domain.CrawlPageAliased, Title: title,
					DocLength: len(trimmed), LinksFound: len(links),
					FetchedAt: attemptedAt, DurationMs: durationMs,
				})
				enqueue(append(links, canonicalURL))
				continue
			}
		}

		doc := domain.Document{ID: documentID(selfURL, v.URLAliasWWWEnabled), URL: selfURL, Title: title, Text: text, Links: links}
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

// linkScopeMatcher decides whether a discovered link is in scope for a
// crawl, relative to its seeds -- see domain.LinkScope*. hosts is each
// seed's exact host; domains its registrable domain (eTLD+1); names its
// domain name with suffix stripped, so LinkScopeTLD ignores TLD.
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

// allows reports whether rawURL is in scope for the crawl's LinkScope.
// Any always allows; Host requires an exact seed-host match; Domain
// (default) allows any host sharing a seed's registrable domain; TLD
// allows any host whose domain name matches regardless of TLD
// (example.com also allows example.org, but not other.com).
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

// registrableDomain returns host's eTLD+1 (e.g. "blog.example.co.uk" ->
// "example.co.uk") via the public suffix list. Falls back to host itself
// (IP, "localhost", bare suffix) when none can be derived.
func registrableDomain(host string) string {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etld1
}

// domainName returns host's registrable domain with its public suffix
// stripped (e.g. "blog.example.co.uk" -> "example"), for LinkScopeTLD.
func domainName(host string) string {
	reg := registrableDomain(host)
	if suffix, _ := publicsuffix.PublicSuffix(host); suffix != "" {
		if name, ok := strings.CutSuffix(reg, "."+suffix); ok {
			return name
		}
	}
	return reg
}

// domainListMatcher checks a host against a per-crawl allow/block list --
// registrable-domain-based, so "example.com" also matches its subdomains.
type domainListMatcher struct {
	domains map[string]bool
}

func newDomainListMatcher(raw []string) domainListMatcher {
	m := domainListMatcher{domains: make(map[string]bool, len(raw))}
	for _, d := range raw {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		m.domains[registrableDomain(d)] = true
	}
	return m
}

func (m domainListMatcher) matches(host string) bool {
	if len(m.domains) == 0 || host == "" {
		return false
	}
	return m.domains[registrableDomain(strings.ToLower(host))]
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

// parseSitemap extracts every <loc> from a sitemap.xml body -- invalid
// XML or a non-urlset body just yields no URLs, never an error.
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

// documentID derives a stable ID from a URL so re-crawling upserts the
// same row. Hashed after CanonicalizeURL, so URLs that canonicalize
// identically (e.g. www vs bare host) produce the same ID.
func documentID(rawURL string, stripWWW bool) string {
	sum := sha256.Sum256([]byte(domain.CanonicalizeURL(rawURL, stripWWW)))
	return "doc-" + hex.EncodeToString(sum[:8])
}
