package application_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/application"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type recordingSQLRepo struct {
	fakeSQLRepo
	saved      []domain.Document
	embeddings [][]float32
	saveErr    error
}

func (r *recordingSQLRepo) SaveDocument(_ context.Context, doc domain.Document, embedding []float32) error {
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
		"http://a": "<html>a</html>",
		"http://b": "<html>b</html>",
	}}
	robots := &fakeRobots{}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}

	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a" {
			return "A", "genuegend inhalt text fuer die seite a hier bitte danke", []string{"http://b"}
		}
		return "B", "genuegend inhalt text fuer die seite b hier auch danke", nil
	}

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, err := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
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

func TestSQLCrawlerService_Crawl_RespectsRobots(t *testing.T) {
	fetcher := &fakeFetcher{pages: map[string]string{"http://a": "<html>a</html>"}}
	robots := &fakeRobots{disallowed: map[string]bool{"http://a": true}}
	repo := &recordingSQLRepo{}
	embedder := &fakeEmbedder{vec: []float32{1, 0}}
	parse := func(html, pageURL string) (string, string, []string) { return "A", "genuegend text inhalt seite", nil }

	svc := application.NewSQLCrawlerService(fetcher, robots, repo, embedder, parse, nil)
	count, _ := svc.Crawl(context.Background(), ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}, nil)
	if count != 0 {
		t.Errorf("expected 0 crawled (robots disallow), got %d", count)
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
