package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
)

type fakeFetcher struct {
	pages map[string]string
	err   map[string]error
}

func (f *fakeFetcher) Fetch(_ context.Context, url string) (string, error) {
	if err, ok := f.err[url]; ok {
		return "", err
	}
	return f.pages[url], nil
}

type fakeRobots struct{ disallowed map[string]bool }

func (r *fakeRobots) Allowed(_ context.Context, url string) bool { return !r.disallowed[url] }

type fakeRepo struct {
	saved   []domain.Document
	saveErr error
}

func (r *fakeRepo) Save(_ context.Context, doc domain.Document) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, doc)
	return nil
}
func (r *fakeRepo) All(_ context.Context) ([]domain.Document, error) { return r.saved, nil }

func TestCrawlerService_Crawl_HappyPath(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a": "<html>a</html>",
		"http://b": "<html>b</html>",
	}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}

	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a" {
			return "A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://b"}
		}
		return "B", "genuegend inhalt text fuer die seite b hier auch danke", nil
	}

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, err := svc.Crawl(context.Background(), []string{"http://a"}, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 crawled pages, got %d", count)
	}
	if len(repo.saved) != 2 || len(idx.addCalls) != 2 {
		t.Errorf("expected saved+indexed docs, repo=%d idx=%d", len(repo.saved), len(idx.addCalls))
	}
}

func TestCrawlerService_Crawl_RespectsRobots(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{disallowed: map[string]bool{"http://a": true}}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) { return "A", "genuegend text inhalt seite", nil }

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"http://a"}, 5)
	if count != 0 {
		t.Errorf("expected 0 crawled (robots disallow), got %d", count)
	}
}

func TestCrawlerService_Crawl_SkipsFetchErrors(t *testing.T) {
	fetcher := &fakeFetcher{err: map[string]error{"http://a": errors.New("boom")}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) { return "", "", nil }

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, err := svc.Crawl(context.Background(), []string{"http://a"}, 5)
	if err != nil || count != 0 {
		t.Errorf("expected graceful skip, got count=%d err=%v", count, err)
	}
}

func TestCrawlerService_Crawl_SkipsThinContent(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) { return "A", "zu kurz", nil }

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"http://a"}, 5)
	if count != 0 {
		t.Errorf("expected thin content skipped, got %d", count)
	}
}

func TestCrawlerService_Crawl_StopsAtMaxPages(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{
		"http://a": "<html>a</html>", "http://b": "<html>b</html>", "http://c": "<html>c</html>",
	}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) {
		next := map[string][]string{"http://a": {"http://b"}, "http://b": {"http://c"}}
		return "T", "genuegend inhalt text fuer diese seite bitte danke", next[pageURL]
	}

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"http://a"}, 2)
	if count != 2 {
		t.Errorf("expected stop at maxPages=2, got %d", count)
	}
}

func TestCrawlerService_Crawl_ZeroMaxPagesDefaultsToTwenty(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"http://a"}, 0)
	if count != 1 {
		t.Errorf("expected maxPages=0 to default and still crawl the single seed, got %d", count)
	}
}

func TestCrawlerService_Crawl_IgnoresNonHTTPURLs(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) { return "", "", nil }

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"ftp://a", "mailto:x@y.com"}, 5)
	if count != 0 {
		t.Errorf("expected non-HTTP URLs ignored, got %d", count)
	}
}

func TestCrawlerService_Crawl_RepoSaveErrorPropagates(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &fakeRepo{saveErr: errors.New("disk full")}
	idx := &fakeIndexer{}
	parse := func(html, pageURL string) (string, string, []string) {
		return "A", "genuegend inhalt text fuer diese seite bitte danke", nil
	}

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	_, err := svc.Crawl(context.Background(), []string{"http://a"}, 5)
	if err == nil {
		t.Error("expected repo error to propagate")
	}
}

func TestCrawlerService_Crawl_DeduplicatesVisitedURLs(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{}
	repo := &fakeRepo{}
	idx := &fakeIndexer{}
	calls := 0
	parse := func(html, pageURL string) (string, string, []string) {
		calls++
		return "A", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a"}
	}

	svc := application.NewCrawlerService(fetcher, robots, repo, idx, parse)
	count, _ := svc.Crawl(context.Background(), []string{"http://a", "http://a"}, 5)
	if count != 1 || calls != 1 {
		t.Errorf("expected deduplication, count=%d calls=%d", count, calls)
	}
}
