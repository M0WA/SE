package application

import (
	"context"
	"fmt"
	"net/url"
	"strings"

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
	fetchOpts := ports.FetchOptions{Cookie: opts.Cookie, BasicAuthUser: opts.BasicAuthUser, BasicAuthPass: opts.BasicAuthPass}

	emit := func(ev domain.CrawlPageEvent) {
		if onPage != nil {
			onPage(ev)
		}
	}

	visited := make(map[string]bool)
	queue := append([]string{}, opts.SeedURLs...)
	crawled := 0

	for len(queue) > 0 && crawled < maxPages {
		u := queue[0]
		queue = queue[1:]

		if visited[u] || !isHTTP(u) {
			continue
		}
		visited[u] = true

		if robots != nil && !robots.Allowed(ctx, u) {
			emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageRobotsDisallowed})
			continue
		}

		html, err := fetcher.FetchWithOptions(ctx, u, fetchOpts)
		if err != nil {
			emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageFetchFailed})
			continue
		}

		title, text, links := parseHTML(html, u)
		if len(strings.TrimSpace(text)) < v.MinTextLength {
			emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageThinContent})
			continue
		}

		doc := domain.Document{ID: fmt.Sprintf("doc-%d", crawled), URL: u, Title: title, Text: text, Links: links}
		if err := save(ctx, doc); err != nil {
			return crawled, err
		}
		crawled++
		emit(domain.CrawlPageEvent{URL: u, Status: domain.CrawlPageIndexed, Title: title})

		for _, l := range links {
			if !visited[l] {
				queue = append(queue, l)
			}
		}
	}
	return crawled, nil
}

func isHTTP(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https")
}
