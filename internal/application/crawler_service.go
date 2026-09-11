package application

import (
	"context"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type crawlerService struct {
	fetcher   ports.AuthFetcher
	robots    ports.RobotsChecker
	repo      ports.Repository
	index     ports.Indexer
	parseHTML func(html, pageURL string) (title, text string, links []string)
	settings  *domain.OperationalSettings
}

func NewCrawlerService(
	fetcher ports.AuthFetcher,
	robots ports.RobotsChecker,
	repo ports.Repository,
	index ports.Indexer,
	parseHTML func(string, string) (string, string, []string),
	settings *domain.OperationalSettings,
) ports.CrawlerService {
	return &crawlerService{fetcher, robots, repo, index, parseHTML, settings}
}

func (c *crawlerService) Crawl(ctx context.Context, opts ports.CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error) {
	return crawlLoop(ctx, c.fetcher, c.robots, c.parseHTML, c.settings, opts, func(ctx context.Context, doc domain.Document) error {
		if err := c.repo.Save(ctx, doc); err != nil {
			return err
		}
		c.index.Add(doc)
		return nil
	}, onPage)
}
