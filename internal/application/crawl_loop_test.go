package application

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

type recordingFetcher struct {
	gotOptions []ports.FetchOptions
	html       string
	err        error
}

func (f *recordingFetcher) FetchWithOptions(_ context.Context, _ string, opts ports.FetchOptions) (string, error) {
	f.gotOptions = append(f.gotOptions, opts)
	if f.err != nil {
		return "", f.err
	}
	return f.html, nil
}

type crawlLoopFakeRobots struct{ disallowed map[string]bool }

func (r *crawlLoopFakeRobots) Allowed(_ context.Context, url string) bool { return !r.disallowed[url] }

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

func TestCrawlLoop_PassesUserAgentToFetcher(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5, UserAgent: "custom-bot/1.0"}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fetcher.gotOptions[0].UserAgent != "custom-bot/1.0" {
		t.Errorf("expected UserAgent to pass through, got %+v", fetcher.gotOptions[0])
	}
}

func TestCrawlLoop_RobotsDisallowedOnlyWhenRespectRobotsSet(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	robots := &crawlLoopFakeRobots{disallowed: map[string]bool{"http://a": true}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	// Default: robots.txt ignored, so the disallowed page still gets crawled.
	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}
	count, err := crawlLoop(context.Background(), fetcher, robots, parse, nil, opts, save, onPage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected robots.txt to be ignored by default, got count=%d", count)
	}
	if len(events) != 1 || events[0].Status != domain.CrawlPageIndexed {
		t.Errorf("expected the page to be indexed, got %+v", events)
	}
}

func TestCrawlLoop_RobotsDisallowedWhenRespectRobotsIsSet(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	robots := &crawlLoopFakeRobots{disallowed: map[string]bool{"http://a": true}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5, RespectRobots: true}
	count, err := crawlLoop(context.Background(), fetcher, robots, parse, nil, opts, save, onPage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected robots.txt to be respected, got count=%d", count)
	}
	if len(events) != 1 || events[0].Status != domain.CrawlPageRobotsDisallowed {
		t.Errorf("expected a robots_disallowed event, got %+v", events)
	}
}

func TestCrawlLoop_FetchFailureIncludesErrorDetail(t *testing.T) {
	fetcher := &recordingFetcher{err: errors.New("dial tcp: connection refused")}
	parse := func(html, pageURL string) (string, string, []string) { return "", "", nil }
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, onPage); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Status != domain.CrawlPageFetchFailed {
		t.Fatalf("expected a fetch_failed event, got %+v", events)
	}
	if events[0].Error != "dial tcp: connection refused" {
		t.Errorf("expected the underlying error message, got %q", events[0].Error)
	}
}

func TestCrawlLoop_ThinContentIncludesLengthDetail(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) { return "T", "too short", nil }
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{MinTextLength: 50})
	opts := ports.CrawlOptions{SeedURLs: []string{"http://a"}, MaxPages: 5}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, settings, opts, save, onPage); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Status != domain.CrawlPageThinContent {
		t.Fatalf("expected a thin_content event, got %+v", events)
	}
	if events[0].Error != "9 characters, need at least 50" {
		t.Errorf("expected a length detail message, got %q", events[0].Error)
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
