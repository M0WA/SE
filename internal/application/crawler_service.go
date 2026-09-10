package application

import (
	"context"

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
	return crawlLoop(ctx, c.fetcher, c.robots, c.parseHTML, seedURLs, maxPages, func(ctx context.Context, doc domain.Document) error {
		if err := c.repo.Save(ctx, doc); err != nil {
			return err
		}
		c.index.Add(doc)
		return nil
	})
}
