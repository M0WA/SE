package application

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type crawlerService struct {
	fetcher   ports.Fetcher
	robots    ports.RobotsChecker
	repo      ports.Repository
	index     ports.Indexer
	parseHTML func(html, pageURL string) (title, text string, links []string)
}

func NewCrawlerService(
	fetcher ports.Fetcher,
	robots ports.RobotsChecker,
	repo ports.Repository,
	index ports.Indexer,
	parseHTML func(string, string) (string, string, []string),
) ports.CrawlerService {
	return &crawlerService{fetcher, robots, repo, index, parseHTML}
}

func (c *crawlerService) Crawl(ctx context.Context, seedURLs []string, maxPages int) (int, error) {
	if maxPages <= 0 {
		maxPages = 20
	}

	visited := make(map[string]bool)
	queue := append([]string{}, seedURLs...)
	crawled := 0

	for len(queue) > 0 && crawled < maxPages {
		u := queue[0]
		queue = queue[1:]

		if visited[u] || !isHTTP(u) {
			continue
		}
		visited[u] = true

		if c.robots != nil && !c.robots.Allowed(ctx, u) {
			continue
		}

		html, err := c.fetcher.Fetch(ctx, u)
		if err != nil {
			continue
		}

		title, text, links := c.parseHTML(html, u)
		if len(strings.TrimSpace(text)) < 50 {
			continue
		}

		doc := domain.Document{ID: fmt.Sprintf("doc-%d", crawled), URL: u, Title: title, Text: text, Links: links}
		if err := c.repo.Save(ctx, doc); err != nil {
			return crawled, err
		}
		c.index.Add(doc)
		crawled++

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
