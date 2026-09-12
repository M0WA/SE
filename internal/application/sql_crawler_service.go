package application

import (
	"context"
	"net/url"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type sqlCrawlerService struct {
	fetcher   ports.AuthFetcher
	robots    ports.RobotsChecker
	repo      ports.SQLRepository
	embedder  ports.EmbeddingProvider
	parseHTML func(html, pageURL string) (title, text string, links []string)
	settings  *domain.OperationalSettings
}

// NewSQLCrawlerService is a CrawlerService that persists crawled documents
// (with their embedding) to a SQL-backed ports.SQLRepository, for use with
// the hybrid (BM25 + semantic) search service.
func NewSQLCrawlerService(
	fetcher ports.AuthFetcher,
	robots ports.RobotsChecker,
	repo ports.SQLRepository,
	embedder ports.EmbeddingProvider,
	parseHTML func(string, string) (string, string, []string),
	settings *domain.OperationalSettings,
) ports.CrawlerService {
	return &sqlCrawlerService{fetcher, robots, repo, embedder, parseHTML, settings}
}

func (c *sqlCrawlerService) Crawl(ctx context.Context, opts ports.CrawlOptions, onPage func(domain.CrawlPageEvent)) (int, error) {
	var isIndexed func(string) bool
	if opts.PrioritizeUnindexed {
		if f, err := c.buildIsIndexed(ctx, opts.SeedURLs); err == nil {
			isIndexed = f
		}
		// A failed lookup just means this crawl proceeds without the
		// prioritization optimization -- not a reason to fail the crawl
		// itself.
	}
	return crawlLoop(ctx, c.fetcher, c.robots, c.parseHTML, c.settings, opts, isIndexed, func(ctx context.Context, doc domain.Document) error {
		embedding, err := c.embedder.Embed(ctx, doc.Title+" "+doc.Text)
		if err != nil {
			return err
		}
		return c.repo.SaveDocument(ctx, doc, embedding)
	}, onPage)
}

// buildIsIndexed fetches every already-indexed document ID for seeds'
// host(s) in one query, then returns a cheap, I/O-free predicate crawlLoop
// can call once per candidate URL -- rather than a query per URL, or
// threading the repository (and a context per call) into the hot loop
// itself.
func (c *sqlCrawlerService) buildIsIndexed(ctx context.Context, seeds []string) (func(string) bool, error) {
	hostSet := make(map[string]bool)
	for _, s := range seeds {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			hostSet[u.Host] = true
		}
	}
	hosts := make([]string, 0, len(hostSet))
	for h := range hostSet {
		hosts = append(hosts, h)
	}
	ids, err := c.repo.DocumentIDsByHost(ctx, hosts)
	if err != nil {
		return nil, err
	}
	indexed := make(map[string]bool, len(ids))
	for _, id := range ids {
		indexed[id] = true
	}
	return func(rawURL string) bool {
		return indexed[documentID(rawURL)]
	}, nil
}
