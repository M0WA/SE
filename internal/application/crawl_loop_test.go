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

	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save)
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
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := fetcher.gotOptions[0]
	if got.Cookie != "" || got.BasicAuthUser != "" || got.BasicAuthPass != "" {
		t.Errorf("expected empty fetch options, got %+v", got)
	}
}
