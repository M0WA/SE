package htmlparser

import (
	"io"
	"net/url"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// resolveHTTPURL resolves raw against base (fragment dropped) and returns
// it only if the result is http/https -- shared by <a href> and <link
// rel="canonical" href> resolution below, which both need exactly this.
func resolveHTTPURL(base *url.URL, raw string) (string, bool) {
	if base == nil {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	abs := base.ResolveReference(u)
	abs.Fragment = ""
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return "", false
	}
	return abs.String(), true
}

// Parse extracts a page's title, visible text, outbound links, and its
// declared canonical URL (see application.crawlLoop, which skips creating
// a document row for a page whose canonical points elsewhere). canonicalURL
// is "" when absent; both it and links are resolved/filtered the same way.
func Parse(r io.Reader, pageURL string) (title, text string, links []string, canonicalURL string) {
	// html.Parse has no charset handling of its own -- its own doc comment
	// requires the caller to already provide UTF-8. charset.NewReader
	// detects the real encoding (a <meta charset> tag or a byte-order mark
	// sniffed from the body; falls back to UTF-8/Windows-1252 per the
	// HTML5 sniffing algorithm if neither is present) and transcodes to
	// UTF-8 first. Without this, a page actually served in a legacy
	// encoding (Windows-1252/ISO-8859-1 -- not rare even on an otherwise
	// modern site, e.g. one legacy embedded widget) leaks raw non-UTF-8
	// bytes straight into title/text, which Postgres's strict UTF8
	// encoding then rejects outright at save time ("invalid byte sequence
	// for encoding UTF8") -- a real crawl failure this fixes. No Content-
	// Type header is passed through (httpfetcher doesn't currently expose
	// one to this layer) -- body-sniffing alone still catches the common
	// case of a page declaring its own charset via a meta tag.
	utf8Reader, err := charset.NewReader(r, "")
	if err != nil {
		utf8Reader = r
	}
	doc, err := html.Parse(utf8Reader)
	if err != nil {
		return "", "", nil, ""
	}

	base, _ := url.Parse(pageURL)
	var sb strings.Builder
	skipDepth := 0

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		isSkipTag := n.Type == html.ElementNode &&
			(n.Data == "script" || n.Data == "style" || n.Data == "noscript" || n.Data == "nav" || n.Data == "footer")

		if isSkipTag {
			skipDepth++
		}

		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if n.FirstChild != nil {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "a":
				for _, a := range n.Attr {
					if a.Key == "href" {
						if resolved, ok := resolveHTTPURL(base, a.Val); ok {
							links = append(links, resolved)
						}
					}
				}
			case "link":
				var rel, href string
				for _, a := range n.Attr {
					switch strings.ToLower(a.Key) {
					case "rel":
						rel = strings.ToLower(strings.TrimSpace(a.Val))
					case "href":
						href = a.Val
					}
				}
				if rel == "canonical" && href != "" {
					if resolved, ok := resolveHTTPURL(base, href); ok {
						canonicalURL = resolved
					}
				}
			}
		}

		if n.Type == html.TextNode && skipDepth == 0 {
			sb.WriteString(n.Data)
			sb.WriteString(" ")
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}

		if isSkipTag {
			skipDepth--
		}
	}
	walk(doc)

	text = strings.Join(strings.Fields(sb.String()), " ")
	if title == "" {
		title = pageURL
	}
	// Defense in depth on top of the charset.NewReader conversion above:
	// guarantees title/text are valid UTF-8 no matter what (a wrong
	// encoding guess, a genuinely mixed-encoding page, or any other
	// upstream surprise), since every downstream consumer -- most strictly
	// Postgres's UTF8 column encoding -- has zero tolerance for anything
	// less. Runs of invalid bytes are replaced with U+FFFD rather than
	// dropped outright, so a save never silently loses unrelated content
	// alongside the bad bytes.
	if !utf8.ValidString(title) {
		title = strings.ToValidUTF8(title, "�")
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	return title, text, links, canonicalURL
}
