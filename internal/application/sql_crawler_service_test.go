package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type fakeFetcher struct {
	pages map[string]string
	err   map[string]error
}

func (f *fakeFetcher) FetchWithOptions(_ context.Context, url string, _ ports.FetchOptions) (string, error) {
	if err, ok := f.err[url]; ok {
		return "", err
	}
	return f.pages[url], nil
}

type fakeRobots struct{ disallowed map[string]bool }

func (r *fakeRobots) Allowed(_ context.Context, url string) bool { return !r.disallowed[url] }

type recordingSQLRepo struct {
	fakeSQLRepo
	// mu guards saved/embeddings -- only needed because
	// TestSQLCrawlerService_Crawl_SharesRateLimitAcrossConcurrentCrawls
	// calls SaveDocument from two concurrently-running Crawl goroutines;
	// every other test here uses this repo from a single goroutine.
	mu         sync.Mutex
	saved      []domain.Document
	embeddings [][]float32
	saveErr    error
}

func (r *recordingSQLRepo) SaveDocument(_ context.Context, doc domain.Document, embedding []float32, _, _ int) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, doc)
	r.embeddings = append(r.embeddings, embedding)
	return nil
}

type erroringEmbedder struct{}

func (erroringEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embed failed")
}
func (erroringEmbedder) Dimensions() int { return 0 }

func TestSQLCrawlerService_Crawl_HappyPath(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a/":  "<html>a</html>",
		"http://a/b": "<html>b</html>",
	}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a/" {
			return "A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://a/b"}
		}
		return "B", "genuegend inhalt text fuer die seite b hier auch danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 5}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 crawled pages, got %d", count)
	}
	if len(repo.saved) != 2 {
		t.Errorf("expected 2 saved docs, got %d", len(repo.saved))
	}
	for _, emb := range repo.embeddings {
		if len(emb) != 2 || emb[0] != 1 {
			t.Errorf("expected embedding to be passed through, got %v", emb)
		}
	}
}

// TestSQLCrawlerService_Crawl_PrioritizeUnindexedConsultsDocumentIDsByHost
// proves the wiring between CrawlOptions.PrioritizeUnindexed and the
// repository: when set, Crawl looks up the seed's already-indexed
// documents via DocumentIDsByHost before crawling (crawlLoop's own tests
// cover the resulting fetch-order behavior in detail; this proves
// sqlCrawlerService actually builds and passes that lookup through).
func TestSQLCrawlerService_Crawl_PrioritizeUnindexedConsultsDocumentIDsByHost(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a/": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer die seite a hier bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	_, err := svc.Crawl(context.Background(), ports.CrawlOptions{
		SeedURLs: []string{"http://a/"}, MaxPages: 5, PrioritizeUnindexed: true,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.documentIDsByHostCalls != 1 {
		t.Errorf("expected PrioritizeUnindexed to trigger exactly one DocumentIDsByHost lookup, got %d", repo.documentIDsByHostCalls)
	}
}

// TestSQLCrawlerService_Crawl_PrioritizeUnindexedFalseSkipsTheLookup
// proves the lookup is only ever done when actually asked for -- no wasted
// query on the common (non-recrawl) path.
func TestSQLCrawlerService_Crawl_PrioritizeUnindexedFalseSkipsTheLookup(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a/": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer die seite a hier bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	_, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 5}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.documentIDsByHostCalls != 0 {
		t.Errorf("expected no DocumentIDsByHost lookup when PrioritizeUnindexed is false, got %d calls", repo.documentIDsByHostCalls)
	}
}

// TestSQLCrawlerService_Crawl_FollowIndexedDomainsConsultsHostsIndexed
// proves the wiring between CrawlOptions.FollowIndexedDomains and the
// repository: when set, Crawl follows an out-of-scope link whose host
// HostsIndexed reports as already indexed (crawlLoop's own tests cover the
// resulting allow/reject decision in detail; this proves sqlCrawlerService
// actually builds and passes that lookup through).
func TestSQLCrawlerService_Crawl_FollowIndexedDomainsConsultsHostsIndexed(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a/":            "<html>a</html>",
		"http://indexed.test/": "<html>b</html>",
	}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{fakeSQLRepo: fakeSQLRepo{hostsIndexedResult: map[string]bool{"indexed.test": true}}}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a/" {
			return "A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://indexed.test/"}
		}
		return "B", "genuegend inhalt text fuer die seite b hier auch danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, err := svc.Crawl(context.Background(), ports.CrawlOptions{
		SeedURLs: []string{"http://a/"}, MaxPages: 5, LinkScope: domain.LinkScopeHost, FollowIndexedDomains: true,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected the already-indexed out-of-scope domain followed, got count=%d", count)
	}
	if repo.hostsIndexedCalls != 1 {
		t.Errorf("expected exactly one HostsIndexed lookup, got %d", repo.hostsIndexedCalls)
	}
}

// TestSQLCrawlerService_Crawl_FollowIndexedDomainsFalseSkipsTheLookup
// proves the lookup is only ever done when actually asked for -- no wasted
// query on the common (FollowIndexedDomains off) path.
func TestSQLCrawlerService_Crawl_FollowIndexedDomainsFalseSkipsTheLookup(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a/": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer die seite a hier bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	_, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 5}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.hostsIndexedCalls != 0 {
		t.Errorf("expected no HostsIndexed lookup when FollowIndexedDomains is false, got %d calls", repo.hostsIndexedCalls)
	}
}

func TestSQLCrawlerService_Crawl_RespectsRobots(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{disallowed: map[string]bool{"http://a": true}}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, _ := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5, RespectRobots: true}, nil)
	if count != 0 {
		t.Errorf("expected 0 crawled (robots disallow), got %d", count)
	}
}

func TestSQLCrawlerService_Crawl_IgnoresRobotsByDefault(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{disallowed: map[string]bool{"http://a": true}}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, _ := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
	if count != 1 {
		t.Errorf("expected robots.txt to be ignored by default, got count=%d", count)
	}
}

func TestSQLCrawlerService_Crawl_SkipsThinContent(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) { return "A", "zu kurz", nil }

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, _ := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
	if count != 0 {
		t.Errorf("expected thin content skipped, got %d", count)
	}
}

func TestSQLCrawlerService_Crawl_SaveErrorPropagates(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{saveErr: errors.New("disk full")}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	_, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
	if err == nil {
		t.Error("expected repo error to propagate")
	}
}

func TestSQLCrawlerService_Crawl_EmbedErrorPropagates(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &erroringEmbedder{}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	_, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
	if err == nil {
		t.Error("expected embed error to propagate")
	}
}

// TestSQLCrawlerService_Crawl_PacesEmbedCalls proves a single crawl job's
// own Embed calls are paced to domain.OperationalSettingsValues.
// EmbeddingRateLimitPerSecond, the same real-provider-rate-limit concern
// application.RunEmbeddingRecomputeJob already paces for.
func TestSQLCrawlerService_Crawl_PacesEmbedCalls(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a/":  "<html>a</html>",
		"http://a/b": "<html>b</html>",
		"http://a/c": "<html>c</html>",
	}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) {
		switch pageURL {
		case "http://a/":
			return "A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://a/b"}
		case "http://a/b":
			return "B", "genuegend inhalt text fuer die seite b hier auch danke", []string{"http://a/c"}
		default:
			return "C", "genuegend inhalt text fuer die seite c hier auch danke", nil
		}
	}
	const ratePerSecond = 10 // 100ms/call
	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{EmbeddingRateLimitPerSecond: ratePerSecond})

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, settings)
	start := time.Now()
	if _, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 5}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 pages saved, each paced to at least 100ms -- expect at least 2
	// full intervals' worth of total wait (between the 1st-2nd and
	// 2nd-3rd calls), with slack for scheduling jitter.
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("expected pacing to add at least ~150ms across 3 pages, took %v", elapsed)
	}
}

// TestSQLCrawlerService_Crawl_SharesRateLimitAcrossConcurrentCrawls proves
// the whole point of embedRateLimiter being a field on sqlCrawlerService
// (constructed once, shared by every concurrently-running crawl job --
// see internal/adapters/restapi/crawl_internal.go's maxConcurrentCrawls):
// two Crawl calls running at the same time against the same service
// instance must share one combined rate budget, not each independently
// pace itself and let their combined call rate exceed the configured
// limit.
func TestSQLCrawlerService_Crawl_SharesRateLimitAcrossConcurrentCrawls(t *testing.T) {
	// One shared service (mirroring cmd/crawl constructing sqlCrawlerService
	// exactly once and reusing it for every job) crawling two independent
	// seeds concurrently -- what matters is that both goroutines' Embed
	// calls draw from the one embedRateLimiter this single *sqlCrawlerService
	// instance holds.
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a/":  "<html>a</html>",
		"http://a/b": "<html>b</html>",
		"http://x/":  "<html>x</html>",
		"http://x/y": "<html>y</html>",
	}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	content := map[string]struct {
		title, text string
		links       []string
	}{
		"http://a/":  {"A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://a/b"}},
		"http://a/b": {"B", "genuegend inhalt text fuer die seite b hier auch danke", nil},
		"http://x/":  {"X", "genuegend inhalt text fuer die seite x hier bitte danke", []string{"http://x/y"}},
		"http://x/y": {"Y", "genuegend inhalt text fuer die seite y hier auch danke", nil},
	}
	parse := func(_, pageURL string) (string, string, []string) {
		c := content[pageURL]
		return c.title, c.text, c.links
	}
	const ratePerSecond = 10 // 100ms/call, shared across both jobs below
	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{EmbeddingRateLimitPerSecond: ratePerSecond})

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, settings)

	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 5}, nil)
	}()
	go func() {
		defer wg.Done()
		_, _ = svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://x/"}, MaxPages: 5}, nil)
	}()
	wg.Wait()
	elapsed := time.Since(start)

	// 4 pages total (2 per job) saved concurrently through one shared
	// limiter at 10/s -- if the limiter is genuinely shared, 4 calls need
	// at least 3 full 100ms spacings (~300ms). If each job paced itself
	// independently instead, both would finish in parallel in ~100ms
	// (1 wait for 2 calls each), clearly distinguishable from the shared
	// case.
	if elapsed < 250*time.Millisecond {
		t.Errorf("expected the rate limit to be shared across both concurrent crawls (~300ms for 4 combined calls at 10/s), took %v -- looks like each job paced independently", elapsed)
	}
	if len(repo.saved) != 4 {
		t.Fatalf("expected 4 total saved documents across both jobs, got %d", len(repo.saved))
	}
}
