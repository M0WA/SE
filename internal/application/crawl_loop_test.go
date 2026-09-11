package application

import (
	"context"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type recordingFetcher struct {
	gotOptions []ports.FetchOptions
	html       string
}

func (f *recordingFetcher) FetchWithOptions(_ context.Context, _ string, opts ports.FetchOptions) (string, error) {
	f.gotOptions = append(f.gotOptions, opts)
	return f.html, nil
}

func TestCrawlLoop_PassesAuthOptionsToFetcher(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{
		SeedURLs:      []string{"http://a"},
		MaxPages:      5,
		Cookie:        "session=xyz",
		BasicAuthUser: "bob",
		BasicAuthPass: "secret",
	}

	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 crawled page, got %d", count)
	}
	if len(fetcher.gotOptions) != 1 {
		t.Fatalf("expected 1 fetch call, got %d", len(fetcher.gotOptions))
	}
	got := fetcher.gotOptions[0]
	if got.Cookie != "session=xyz" || got.BasicAuthUser != "bob" || got.BasicAuthPass != "secret" {
		t.Errorf("expected auth options to pass through, got %+v", got)
	}
}

func TestCrawlLoop_NoAuthOptionsMeansEmptyFetchOptions(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := fetcher.gotOptions[0]
	if got.Cookie != "" || got.BasicAuthUser != "" || got.BasicAuthPass != "" {
		t.Errorf("expected empty fetch options, got %+v", got)
	}
}

func TestDocumentID_DeterministicForSameURL(t *testing.T) {
	a := documentID("https://example.com/page")
	b := documentID("https://example.com/page")
	if a != b {
		t.Errorf("expected the same URL to always produce the same ID, got %q and %q", a, b)
	}
}

func TestDocumentID_DiffersForDifferentURLs(t *testing.T) {
	a := documentID("https://example.com/page1")
	b := documentID("https://example.com/page2")
	if a == b {
		t.Errorf("expected different URLs to produce different IDs, both got %q", a)
	}
}

func TestCrawlLoop_RecrawlingSameURLReusesSameDocumentID(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	var savedIDs []string
	save := func(ctx context.Context, doc domain.Document) error {
		savedIDs = append(savedIDs, doc.ID)
		return nil
	}

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 1}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil); err != nil {
		t.Fatalf("unexpected error on first crawl: %v", err)
	}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil); err != nil {
		t.Fatalf("unexpected error on second crawl: %v", err)
	}

	if len(savedIDs) != 2 || savedIDs[0] != savedIDs[1] {
		t.Errorf("expected re-crawling the same URL to reuse the same document ID, got %v", savedIDs)
	}
}
