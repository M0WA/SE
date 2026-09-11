package application

import (
	"context"
	"errors"
	"testing"
	"time"

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

// scopedFetcher is a fetcher fake for tests that need per-URL
// responses/errors and to record which URLs were fetched and when --
// domain-scoping, politeness-delay, and sitemap-discovery tests all need
// more than the single-html recordingFetcher above provides.
type scopedFetcher struct {
	pages map[string]string
	errs  map[string]error
	urls  []string
	times []time.Time
}

func (f *scopedFetcher) FetchWithOptions(_ context.Context, url string, _ ports.FetchOptions) (string, error) {
	f.urls = append(f.urls, url)
	f.times = append(f.times, time.Now())
	if err, ok := f.errs[url]; ok {
		return "", err
	}
	return f.pages[url], nil
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

func TestCrawlLoop_IndexedEventIncludesDiagnosticDetail(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a/b", "http://a/c"}
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	before := time.Now()
	opts := ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 1}
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, onPage); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now()

	if len(events) != 1 || events[0].Status != domain.CrawlPageIndexed {
		t.Fatalf("expected 1 indexed event, got %+v", events)
	}
	ev := events[0]
	if ev.DocLength != len("genuegend inhalt text fuer diese seite bitte danke") {
		t.Errorf("expected DocLength to reflect the extracted text, got %d", ev.DocLength)
	}
	if ev.LinksFound != 2 {
		t.Errorf("expected LinksFound=2, got %d", ev.LinksFound)
	}
	if ev.FetchedAt.Before(before) || ev.FetchedAt.After(after) {
		t.Errorf("expected FetchedAt within the test's time window, got %v (window %v..%v)", ev.FetchedAt, before, after)
	}
	if ev.DurationMs < 0 {
		t.Errorf("expected a non-negative DurationMs, got %d", ev.DurationMs)
	}
}

func TestCrawlLoop_RobotsDisallowedEventHasNoDuration(t *testing.T) {
	fetcher := &recordingFetcher{html: "<html>ok</html>"}
	robots := &crawlLoopFakeRobots{disallowed: map[string]bool{"http://a/": true}}
	parse := func(html, pageURL string) (string, string, []string) { return "", "", nil }
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	var events []domain.CrawlPageEvent
	onPage := func(ev domain.CrawlPageEvent) { events = append(events, ev) }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a/"}, MaxPages: 1, RespectRobots: true}
	if _, err := crawlLoop(context.Background(), fetcher, robots, parse, nil, opts, save, onPage); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].DurationMs != 0 {
		t.Errorf("expected a robots_disallowed event with DurationMs=0 (no fetch attempted), got %+v", events)
	}
	if events[0].FetchedAt.IsZero() {
		t.Error("expected FetchedAt to still be set even though no fetch happened")
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

func TestCrawlLoop_StaysOnSeedDomainByDefault(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start": "<html>start</html>",
		"http://a.example/other": "<html>other</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a.example/start" {
			return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a.example/other", "http://b.example/x"}
		}
		return "T2", "genuegend inhalt text fuer diese andere seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 pages crawled (seed + same-domain link), got %d", count)
	}
	for _, u := range fetcher.urls {
		if u == "http://b.example/x" {
			t.Error("expected the off-domain link not to be fetched by default")
		}
	}
}

func TestCrawlLoop_AllowOffDomainLinksFollowsOtherDomains(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start": "<html>start</html>",
		"http://a.example/other": "<html>other</html>",
		"http://b.example/x":     "<html>x</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a.example/start" {
			return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a.example/other", "http://b.example/x"}
		}
		return "T2", "genuegend inhalt text fuer diese andere seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10, AllowOffDomainLinks: true}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected all 3 pages crawled when off-domain links are allowed, got %d", count)
	}
}

func TestCrawlLoop_MultipleSeedsAllCountAsOnDomain(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start": "<html>a</html>",
		"http://b.example/start": "<html>b</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start", "http://b.example/start"}, MaxPages: 10}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected both seeds' own hosts treated as on-domain, got %d", count)
	}
}

func TestCrawlLoop_WaitsCrawlDelayBetweenFetches(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a/1": "<html>a</html>",
		"http://a/2": "<html>b</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a/1" {
			return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a/2"}
		}
		return "T2", "genuegend inhalt text fuer diese andere seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }
	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{CrawlDelayMs: 30})

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a/1"}, MaxPages: 5}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, settings, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 pages crawled, got %d", count)
	}
	if len(fetcher.times) != 2 {
		t.Fatalf("expected 2 fetch calls, got %d", len(fetcher.times))
	}
	if gap := fetcher.times[1].Sub(fetcher.times[0]); gap < 25*time.Millisecond {
		t.Errorf("expected at least ~30ms between fetches, got %v", gap)
	}
}

func TestCrawlLoop_ZeroCrawlDelayMeansNoWait(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a/1": "<html>a</html>",
		"http://a/2": "<html>b</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a/1" {
			return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a/2"}
		}
		return "T2", "genuegend inhalt text fuer diese andere seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }
	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{CrawlDelayMs: 0})

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a/1"}, MaxPages: 5}
	start := time.Now()
	if _, err := crawlLoop(context.Background(), fetcher, nil, parse, settings, opts, save, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Errorf("expected no delay with CrawlDelayMs=0, took %v", elapsed)
	}
}

func TestCrawlLoop_CancelledContextDuringDelayStopsTheCrawl(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a/1": "<html>a</html>",
		"http://a/2": "<html>b</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		if pageURL == "http://a/1" {
			return "T", "genuegend inhalt text fuer diese seite bitte danke", []string{"http://a/2"}
		}
		return "T2", "genuegend inhalt text fuer diese andere seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }
	settings := domain.NewOperationalSettings(domain.OperationalSettingsValues{CrawlDelayMs: 200})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a/1"}, MaxPages: 5}
	count, err := crawlLoop(ctx, fetcher, nil, parse, settings, opts, save, nil)
	if err == nil {
		t.Fatal("expected the cancelled context to surface as an error")
	}
	if count != 1 {
		t.Errorf("expected the first page still counted before cancellation, got %d", count)
	}
	if len(fetcher.times) != 1 {
		t.Errorf("expected only the first fetch to happen before cancellation, got %d calls", len(fetcher.times))
	}
}

func TestCrawlLoop_UseSitemapEnqueuesDiscoveredURLs(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start":        "<html>start</html>",
		"http://a.example/sitemap.xml":  `<?xml version="1.0"?><urlset><url><loc>http://a.example/from-sitemap</loc></url></urlset>`,
		"http://a.example/from-sitemap": "<html>from sitemap</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10, UseSitemap: true}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected the seed plus the sitemap-discovered URL crawled, got %d", count)
	}
}

func TestCrawlLoop_SitemapURLsRespectOffDomainRule(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start":       "<html>start</html>",
		"http://a.example/sitemap.xml": `<?xml version="1.0"?><urlset><url><loc>http://other.example/x</loc></url></urlset>`,
		"http://other.example/x":       "<html>x</html>",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10, UseSitemap: true}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected the off-domain sitemap URL to be skipped, got count=%d", count)
	}
}

func TestCrawlLoop_SitemapNotFetchedWhenUseSitemapIsFalse(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start":       "<html>start</html>",
		"http://a.example/sitemap.xml": `<?xml version="1.0"?><urlset><url><loc>http://a.example/from-sitemap</loc></url></urlset>`,
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected only the seed crawled without use_sitemap, got %d", count)
	}
	for _, u := range fetcher.urls {
		if u == "http://a.example/sitemap.xml" {
			t.Error("expected sitemap.xml not to be fetched when use_sitemap is false")
		}
	}
}

func TestCrawlLoop_MissingSitemapIsSkippedSilently(t *testing.T) {
	fetcher := &scopedFetcher{
		pages: map[string]string{"http://a.example/start": "<html>start</html>"},
		errs:  map[string]error{"http://a.example/sitemap.xml": errors.New("404 not found")},
	}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10, UseSitemap: true}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected only the seed crawled when sitemap.xml can't be fetched, got %d", count)
	}
}

func TestCrawlLoop_UnparseableSitemapIsSkippedSilently(t *testing.T) {
	fetcher := &scopedFetcher{pages: map[string]string{
		"http://a.example/start":       "<html>start</html>",
		"http://a.example/sitemap.xml": "not xml at all",
	}}
	parse := func(html, pageURL string) (string, string, []string) {
		return "T", "genuegend inhalt text fuer diese seite bitte danke", nil
	}
	save := func(ctx context.Context, doc domain.Document) error { return nil }

	opts := ports.CrawlOptions{SeedURLs: []string{"http://a.example/start"}, MaxPages: 10, UseSitemap: true}
	count, err := crawlLoop(context.Background(), fetcher, nil, parse, nil, opts, save, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected only the seed crawled when sitemap.xml is unparseable, got %d", count)
	}
}

func TestSitemapURL_BuildsOriginPath(t *testing.T) {
	got, ok := sitemapURL("https://example.com/some/path?x=1#frag")
	if !ok || got != "https://example.com/sitemap.xml" {
		t.Errorf("expected https://example.com/sitemap.xml, got %q (ok=%v)", got, ok)
	}
}

func TestSitemapURL_RejectsNonHTTPScheme(t *testing.T) {
	if _, ok := sitemapURL("ftp://example.com"); ok {
		t.Error("expected a non-HTTP(S) scheme to be rejected")
	}
}

func TestParseSitemap_ExtractsLocs(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>http://example.com/a</loc></url>
  <url><loc>http://example.com/b</loc></url>
</urlset>`
	urls := parseSitemap(body)
	if len(urls) != 2 || urls[0] != "http://example.com/a" || urls[1] != "http://example.com/b" {
		t.Errorf("unexpected URLs: %v", urls)
	}
}

func TestParseSitemap_InvalidXMLReturnsNil(t *testing.T) {
	if urls := parseSitemap("not xml"); urls != nil {
		t.Errorf("expected nil for invalid XML, got %v", urls)
	}
}
