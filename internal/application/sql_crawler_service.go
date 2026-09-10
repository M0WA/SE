package application

import (
	"context"

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
	return crawlLoop(ctx, c.fetcher, c.robots, c.parseHTML, seedURLs, maxPages, func(ctx context.Context, doc domain.Document) error {
		embedding, err := c.embedder.Embed(ctx, doc.Title+" "+doc.Text)
		if err != nil {
			return err
		}
		return c.repo.SaveDocument(ctx, doc, embedding)
	})
}
