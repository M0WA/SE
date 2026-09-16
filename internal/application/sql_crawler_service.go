package application

import (
	"context"
	"net/url"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type sqlCrawlerService struct {
	fetcher ports.AuthFetcher
	robots  ports.RobotsChecker
	repo    ports.SQLRepository
	// embedders holds one ports.EmbeddingProvider per currently-enabled
	// provider (domain.EmbeddingProviderHash, or a configured domain.
	// EmbeddingHTTPEndpoint's ID), keyed by provider name -- every enabled
	// provider gets its own embedding computed and stored for every saved
	// document, not just whichever one is currently active for search.
	embedders map[string]ports.EmbeddingProvider
	// rateLimits gives each provider in embedders its own requests-per-
	// second cap (domain.EmbeddingHTTPEndpoint.RateLimitPerSecond; the
	// built-in hash provider is simply absent here, same as ratePerSecond
	// <= 0 -- a local computation with no rate limit of its own). A
	// provider missing from this map is treated the same as 0 (unlimited).
	rateLimits map[string]float64
	parseHTML  func(html, pageURL string) (title, text string, links []string, canonicalURL string)
	settings   *domain.OperationalSettings
	// embedRates paces every Embed call this service makes, one
	// *embedRateLimiter per provider in embedders (see its own doc
	// comment) -- shared across every concurrently running crawl job,
	// since a single sqlCrawlerService is constructed once and reused for
	// all of them (see cmd/crawl/main.go), so a provider's own limit is
	// respected across the combined rate of every job, not per-job.
	embedRates map[string]*embedRateLimiter
}

// NewSQLCrawlerService is a CrawlerService that persists crawled documents
// (with their embeddings) to a SQL-backed ports.SQLRepository, for use with
// the hybrid (BM25 + semantic) search service. rateLimits gives each
// provider in embedders its own requests-per-second cap -- see
// bootstrap.NewEmbedders' caller for how it's built from the currently
// enabled domain.EmbeddingHTTPEndpoint list.
func NewSQLCrawlerService(
	fetcher ports.AuthFetcher,
	robots ports.RobotsChecker,
	repo ports.SQLRepository,
	embedders map[string]ports.EmbeddingProvider,
	rateLimits map[string]float64,
	parseHTML func(string, string) (string, string, []string, string),
	settings *domain.OperationalSettings,
) ports.CrawlerService {
	embedRates := make(map[string]*embedRateLimiter, len(embedders))
	for provider := range embedders {
		embedRates[provider] = &embedRateLimiter{}
	}
	return &sqlCrawlerService{
		fetcher: fetcher, robots: robots, repo: repo,
		embedders: embedders, rateLimits: rateLimits, embedRates: embedRates,
		parseHTML: parseHTML, settings: settings,
	}
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
	var isDomainIndexed func([]string) map[string]bool
	if opts.FollowIndexedDomains {
		isDomainIndexed = c.buildDomainIndexed(ctx)
	}
	return crawlLoop(ctx, c.fetcher, c.robots, c.parseHTML, c.settings, opts, isIndexed, isDomainIndexed, func(ctx context.Context, doc domain.Document) error {
		v := c.settings.Get()
		embeddings := make(map[string][]float32, len(c.embedders))
		for provider, embedder := range c.embedders {
			rate := c.embedRates[provider]
			embed := func(ctx context.Context, s string) ([]float32, error) {
				if rate != nil {
					rate.wait(ctx, c.rateLimits[provider])
				}
				return embedder.Embed(ctx, s)
			}
			vec, err := embedTitleWeighted(ctx, embed, doc.Title, doc.Text, v.EmbeddingTitleWeight)
			if err != nil {
				return err
			}
			embeddings[provider] = vec
		}
		return c.repo.SaveDocument(ctx, doc, embeddings, v.MaxDocumentVersions, v.TitleWeight)
	}, func(ctx context.Context, aliasURL, canonicalID string) error {
		return c.repo.RecordDocumentAlias(ctx, aliasURL, canonicalID, domain.DocumentAliasReasonCanonicalTag)
	}, onPage)
}

// buildDomainIndexed returns a closure crawlLoop calls, at most once per
// enqueue batch, to check whether any already-indexed document exists for
// a discovered link's host -- backing opts.FollowIndexedDomains. Memoizes
// per host across the whole crawl (a per-instance cache, safe for
// crawlLoop's single-goroutine call pattern), so a domain seen repeatedly
// across many pages' links is only ever looked up once. A failed lookup
// for a given host is cached as "not indexed" rather than retried -- same
// tradeoff as buildIsIndexed's own failure handling: proceed without the
// optimization rather than fail the crawl or hammer a failing repository.
func (c *sqlCrawlerService) buildDomainIndexed(ctx context.Context) func([]string) map[string]bool {
	cache := make(map[string]bool)
	return func(hosts []string) map[string]bool {
		var missing []string
		for _, h := range hosts {
			if _, ok := cache[h]; !ok {
				missing = append(missing, h)
			}
		}
		if len(missing) > 0 {
			indexed, err := c.repo.HostsIndexed(ctx, missing)
			for _, h := range missing {
				cache[h] = err == nil && indexed[h]
			}
		}
		result := make(map[string]bool, len(hosts))
		for _, h := range hosts {
			result[h] = cache[h]
		}
		return result
	}
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
	stripWWW := c.settings.Get().URLAliasWWWEnabled
	return func(rawURL string) bool {
		return indexed[documentID(rawURL, stripWWW)]
	}, nil
}
