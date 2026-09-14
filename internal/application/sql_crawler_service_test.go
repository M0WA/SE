package application_test

import (
	"context"
	"errors"
	"testing"

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
	saved      []domain.Document
	embeddings [][]float32
	saveErr    error
}

func (r *recordingSQLRepo) SaveDocument(_ context.Context, doc domain.Document, embedding []float32, _ int) error {
	if r.saveErr != nil {
		return r.saveErr
	}
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
