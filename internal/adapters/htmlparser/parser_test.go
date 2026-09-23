package htmlparser_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"searchengine/internal/adapters/htmlparser"
)

func TestParse_ExtractsTitleTextAndLinks(t *testing.T) {
	raw := `<html><head><title>Testseite</title>
        <style>.x{color:red}</style></head>
        <body>
        <script>console.log('x')</script>
        <nav>Menu</nav>
        <p>Hallo Welt</p>
        <a href="/relativ">Link</a>
        <a href="https://extern.example/seite">Extern</a>
        <footer>Impressum</footer>
        </body></html>`

	title, text, links, canonicalURL := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")

	if title != "Testseite" {
		t.Errorf("expected title 'Testseite', got %q", title)
	}
	if !strings.Contains(text, "Hallo Welt") {
		t.Errorf("expected body text present, got %q", text)
	}
	if strings.Contains(text, "console.log") || strings.Contains(text, "Menu") || strings.Contains(text, "Impressum") {
		t.Errorf("expected script/nav/footer excluded, got %q", text)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d: %v", len(links), links)
	}
	if links[0] != "https://basis.example/relativ" {
		t.Errorf("expected resolved relative link, got %q", links[0])
	}
	if canonicalURL != "" {
		t.Errorf("expected no canonical URL when there is no rel=canonical tag, got %q", canonicalURL)
	}
}

func TestParse_MissingTitleFallsBackToURL(t *testing.T) {
	title, _, _, _ := htmlparser.Parse(strings.NewReader("<html><body>x</body></html>"), "https://x.example/p")
	if title != "https://x.example/p" {
		t.Errorf("expected fallback title, got %q", title)
	}
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, errors.New("read error") }

func TestParse_ReaderErrorReturnsEmpty(t *testing.T) {
	title, text, links, canonicalURL := htmlparser.Parse(failingReader{}, "https://x.example")
	if title != "" || text != "" || links != nil || canonicalURL != "" {
		t.Errorf("expected empty result on read error, got %q %q %v %q", title, text, links, canonicalURL)
	}
}

func TestParse_MalformedURLInHrefIgnored(t *testing.T) {
	raw := `<html><body><a href="http://[::invalid">x</a></body></html>`
	_, _, links, _ := htmlparser.Parse(strings.NewReader(raw), "https://basis.example")
	if len(links) != 0 {
		t.Errorf("expected invalid href ignored, got %v", links)
	}
}

// TestParse_ExtractsCanonicalLinkTag proves a rel=canonical href resolves
// like an <a href> (relative hrefs, fragments dropped) -- crawlLoop
// compares it against the page's own normalized URL to detect an alias.
func TestParse_ExtractsCanonicalLinkTag(t *testing.T) {
	raw := `<html><head><link rel="canonical" href="/de/seite#abschnitt"></head><body>x</body></html>`
	_, _, _, canonicalURL := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")
	if canonicalURL != "https://basis.example/de/seite" {
		t.Errorf("expected resolved canonical URL with fragment dropped, got %q", canonicalURL)
	}
}

func TestParse_CanonicalLinkTagAbsoluteAndCaseInsensitiveRel(t *testing.T) {
	raw := `<html><head><link REL="Canonical" href="https://other.example/page"></head><body>x</body></html>`
	_, _, _, canonicalURL := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")
	if canonicalURL != "https://other.example/page" {
		t.Errorf("expected the absolute canonical URL, case-insensitive rel matched, got %q", canonicalURL)
	}
}

func TestParse_NonCanonicalLinkTagIgnored(t *testing.T) {
	raw := `<html><head><link rel="stylesheet" href="/style.css"></head><body>x</body></html>`
	_, _, _, canonicalURL := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")
	if canonicalURL != "" {
		t.Errorf("expected a non-canonical <link> tag to be ignored, got %q", canonicalURL)
	}
}

func TestParse_MalformedCanonicalHrefIgnored(t *testing.T) {
	raw := `<html><head><link rel="canonical" href="http://[::invalid"></head><body>x</body></html>`
	_, _, _, canonicalURL := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")
	if canonicalURL != "" {
		t.Errorf("expected a malformed canonical href ignored, got %q", canonicalURL)
	}
}

// TestParse_LegacyEncodingViaMetaCharsetIsTranscodedToUTF8 regression-tests
// a real crawl failure: html.Parse requires UTF-8, so a legacy-encoded
// page used to leak raw bytes into text, which Postgres then rejected.
// Windows-1252 encodes "ü"/"ß" as single bytes (0xFC, 0xDF) invalid on
// their own in UTF-8; a <meta charset> page must still transcode correctly.
func TestParse_LegacyEncodingViaMetaCharsetIsTranscodedToUTF8(t *testing.T) {
	raw := []byte("<html><head><meta charset=\"windows-1252\"></head><body><p>Gr\xfc\xdfe</p></body></html>")
	_, text, _, _ := htmlparser.Parse(bytes.NewReader(raw), "https://basis.example/start")
	if !utf8.ValidString(text) {
		t.Fatalf("expected valid UTF-8 output, got invalid bytes: %q", text)
	}
	if !strings.Contains(text, "Grüße") {
		t.Errorf("expected the windows-1252 bytes correctly transcoded to \"Grüße\", got %q", text)
	}
}

// TestParse_InvalidUTF8BytesReplacedWithoutPropagating tests the defensive
// backstop on top of charset detection: a declared-UTF-8 page with a
// stray invalid byte must still come out as valid UTF-8, an absolute
// guarantee since Postgres has zero tolerance otherwise.
func TestParse_InvalidUTF8BytesReplacedWithoutPropagating(t *testing.T) {
	raw := []byte("<html><head><meta charset=\"utf-8\"></head><body><p>Berlin\xa0Wahl</p></body></html>")
	title, text, _, _ := htmlparser.Parse(bytes.NewReader(raw), "https://basis.example/start")
	if !utf8.ValidString(title) || !utf8.ValidString(text) {
		t.Fatalf("expected valid UTF-8 output, got title=%q text=%q", title, text)
	}
	if !strings.Contains(text, "Berlin") || !strings.Contains(text, "Wahl") {
		t.Errorf("expected the surrounding valid text preserved around the bad byte, got %q", text)
	}
}
