package htmlparser_test

import (
	"errors"
	"strings"
	"testing"

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

	title, text, links := htmlparser.Parse(strings.NewReader(raw), "https://basis.example/start")

	if title != "Testseite" {
		t.Errorf("erwartet Titel 'Testseite', bekam %q", title)
	}
	if !strings.Contains(text, "Hallo Welt") {
		t.Errorf("erwartet Body-Text vorhanden, bekam %q", text)
	}
	if strings.Contains(text, "console.log") || strings.Contains(text, "Menu") || strings.Contains(text, "Impressum") {
		t.Errorf("erwartet script/nav/footer ausgeschlossen, bekam %q", text)
	}
	if len(links) != 2 {
		t.Fatalf("erwartet 2 Links, bekam %d: %v", len(links), links)
	}
	if links[0] != "https://basis.example/relativ" {
		t.Errorf("erwartet aufgelösten relativen Link, bekam %q", links[0])
	}
}

func TestParse_MissingTitleFallsBackToURL(t *testing.T) {
	title, _, _ := htmlparser.Parse(strings.NewReader("<html><body>x</body></html>"), "https://x.example/p")
	if title != "https://x.example/p" {
		t.Errorf("erwartet Fallback-Titel, bekam %q", title)
	}
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, errors.New("lesefehler") }

func TestParse_ReaderErrorReturnsEmpty(t *testing.T) {
	title, text, links := htmlparser.Parse(failingReader{}, "https://x.example")
	if title != "" || text != "" || links != nil {
		t.Errorf("erwartet leeres Ergebnis bei Lesefehler, bekam %q %q %v", title, text, links)
	}
}

func TestParse_MalformedURLInHrefIgnored(t *testing.T) {
	raw := `<html><body><a href="http://[::invalid">x</a></body></html>`
	_, _, links := htmlparser.Parse(strings.NewReader(raw), "https://basis.example")
	if len(links) != 0 {
		t.Errorf("erwartet ignorierten ungültigen href, bekam %v", links)
	}
}
