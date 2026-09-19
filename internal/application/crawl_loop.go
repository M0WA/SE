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
	parseHTML func(html, pageURL string) (title, text string, links []string, canonicalURL string),
	settings *domain.OperationalSettings,
	opts ports.CrawlOptions,
	isIndexed func(url string) bool,
	// isDomainIndexed batch-checks whether any given host already has an
	// indexed document, for opts.FollowIndexedDomains -- nil when off.
	isDomainIndexed func(hosts []string) map[string]bool,
	save func(ctx context.Context, doc domain.Document) error,
	// recordAlias persists that aliasURL's content lives under canonicalID,
	// called instead of save when a page's <link rel="canonical"> resolves
	// elsewhere. nil-safe: without it, an aliased page is just saved as
	// its own document (pre-this-feature behavior).
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
	allowlist := newDomainListMatcher(opts.AllowedDomains)
	blocklist := newDomainListMatcher(opts.BlockedDomains)

	// prioritize is true only when PrioritizeUnindexed is set and isIndexed
	// is available. freshQueue/knownQueue split discovery so fresh URLs
	// always dequeue first, spending a bounded maxPages budget on new
	// content before refreshing what's already indexed. When false, every
	// URL lands in freshQueue -- the original single FIFO queue.
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
	// enqueue precedence: BlockedDomains always rejects; then LinkScope or
	// AllowedDomains allows; finally, if isDomainIndexed is available, a
	// link whose host already has an indexed document is allowed too (it
	// only widens scope, never narrows it). isDomainIndexed is called once
	// per batch over distinct undecided hosts, not once per link.
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
		// A <link rel="canonical"> naming a different URL means this
		// page's content only ever indexes under that URL -- no document
		// row is created for u. Its outbound links are enqueued as usual,
		// plus the canonical target itself: without this, the target is
		// only ever crawled if it happens to also be linked from elsewhere,
		// which real sites frequently don't do for their own canonical URLs
		// (e.g. one stripped of tracking params), leaving most aliases
		// permanently dangling -- recorded, but never resolving to an
		// actual indexed document.
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
// crawl, relative to its seeds -- see domain.LinkScope*. hosts holds each
// seed's exact host; domains holds each seed's registrable domain
// (eTLD+1); names holds each seed's domain name with suffix stripped, so
// LinkScopeTLD matching ignores which TLD a link uses.
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

// allows reports whether rawURL is in scope for the crawl's resolved
// LinkScope. LinkScopeAny always allows; LinkScopeHost requires an exact
// seed-host match; LinkScopeDomain (default) allows any host sharing a
// seed's registrable domain; LinkScopeTLD allows any host whose domain
// name matches a seed's regardless of TLD (example.com also allows
// example.org, www.example.co.uk, but not other.com).
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
// "example.co.uk"), using the public suffix list so multi-part TLDs are
// handled correctly. Falls back to host itself (an IP, "localhost", or a
// bare public suffix) when the list can't derive one -- such a host still
// only ever matches itself.
func registrableDomain(host string) string {
	etld1, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return host
	}
	return etld1
}

// domainName returns host's registrable domain with its public suffix
// stripped (e.g. "blog.example.co.uk" -> "example"), for LinkScopeTLD --
// ignores both subdomain and TLD. Falls back to registrableDomain's own
// fallback when no recognized suffix can be stripped.
func domainName(host string) string {
	reg := registrableDomain(host)
	if suffix, _ := publicsuffix.PublicSuffix(host); suffix != "" {
		if name, ok := strings.CutSuffix(reg, "."+suffix); ok {
			return name
		}
	}
	return reg
}

// domainListMatcher checks a link's host against a per-crawl allow/block
// list -- registrable-domain-based, so listing "example.com" also matches
// its subdomains. An empty list never matches anything.
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

// documentID derives a stable ID from a URL so re-crawling the same page
// always upserts the same row. rawURL is hashed after CanonicalizeURL, so
// URLs that canonicalize identically (e.g. www vs bare host, when stripWWW
// is true) produce the same ID with no separate alias bookkeeping needed.
func documentID(rawURL string, stripWWW bool) string {
	sum := sha256.Sum256([]byte(domain.CanonicalizeURL(rawURL, stripWWW)))
	return "doc-" + hex.EncodeToString(sum[:8])
}
