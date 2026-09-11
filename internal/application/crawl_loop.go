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
	save func(ctx context.Context, doc domain.Document) error,
	onPage func(domain.CrawlPageEvent),
) (int, error) {
	v := settings.Get()
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = v.DefaultMaxPages
	}
	fetchOpts := ports.FetchOptions{
		Cookie: opts.Cookie, BasicAuthUser: opts.BasicAuthUser, BasicAuthPass: opts.BasicAuthPass,
		UserAgent: opts.UserAgent,
	}

	emit := func(ev domain.CrawlPageEvent) {
		if onPage != nil {
			onPage(ev)
		}
	}

	hosts := seedHosts(opts.SeedURLs)
	enqueue := func(queue []string, links []string) []string {
		for _, l := range links {
			if opts.AllowOffDomainLinks || onDomain(l, hosts) {
				queue = append(queue, l)
			}
		}
		return queue
	}

	visited := make(map[string]bool)
	queue := append([]string{}, opts.SeedURLs...)

	if opts.UseSitemap {
		for _, seed := range opts.SeedURLs {
			sitemap, ok := sitemapURL(seed)
			if !ok {
				continue
			}
			body, err := fetcher.FetchWithOptions(ctx, sitemap, fetchOpts)
			if err != nil {
				continue
			}
			queue = enqueue(queue, parseSitemap(body))
		}
	}

	crawled := 0
	fetchCount := 0

	for len(queue) > 0 && crawled < maxPages {
		u := queue[0]
		queue = queue[1:]

		if visited[u] || !isHTTP(u) {
			continue
		}
		visited[u] = true
		attemptedAt := time.Now()

		if opts.RespectRobots && robots != nil && !robots.Allowed(ctx, u) {
			emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageRobotsDisallowed, FetchedAt: attemptedAt})
			continue
		}

		if fetchCount > 0 && v.CrawlDelayMs > 0 {
			select {
			case <-time.After(time.Duration(v.CrawlDelayMs) * time.Millisecond):
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
		if len(trimmed) < v.MinTextLength {
			emit(domain.CrawlPageEvent{
				URL: u, Status: domain.CrawlPageThinContent,
				Error:     fmt.Sprintf("%d characters, need at least %d", len(trimmed), v.MinTextLength),
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

		queue = enqueue(queue, links)
	}
	return crawled, nil
}

// seedHosts collects the host of every seed URL, so discovered links can be
// checked against the crawl's own starting point(s) rather than growing to
// include every domain a crawl happens to wander onto.
func seedHosts(seeds []string) map[string]bool {
	hosts := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			hosts[u.Host] = true
		}
	}
	return hosts
}

func onDomain(rawURL string, hosts map[string]bool) bool {
	u, err := url.Parse(rawURL)
	return err == nil && hosts[u.Host]
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
