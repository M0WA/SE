package application

import (
	"context"
	"fmt"
	"strings"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type sqlCrawlerService struct {
	fetcher   ports.Fetcher
	robots    ports.RobotsChecker
	repo      ports.SQLRepository
	embedder  ports.EmbeddingProvider
	parseHTML func(html, pageURL string) (title, text string, links []string)
}

// NewSQLCrawlerService is a CrawlerService that persists crawled documents
// (with their embedding) to a SQL-backed ports.SQLRepository, for use with
// the hybrid (BM25 + semantic) search service.
func NewSQLCrawlerService(
	fetcher ports.Fetcher,
	robots ports.RobotsChecker,
	repo ports.SQLRepository,
	embedder ports.EmbeddingProvider,
	parseHTML func(string, string) (string, string, []string),
) ports.CrawlerService {
	return &sqlCrawlerService{fetcher, robots, repo, embedder, parseHTML}
}

func (c *sqlCrawlerService) Crawl(ctx context.Context, seedURLs []string, maxPages int) (int, error) {
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
		embedding, err := c.embedder.Embed(ctx, title+" "+text)
		if err != nil {
			return crawled, err
		}
		if err := c.repo.SaveDocument(ctx, doc, embedding); err != nil {
			return crawled, err
		}
		crawled++

		for _, l := range links {
			if !visited[l] {
				queue = append(queue, l)
			}
		}
	}
	return crawled, nil
}
