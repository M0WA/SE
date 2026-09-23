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
	// html.Parse requires UTF-8 input. charset.NewReader detects the real
	// encoding (<meta charset>, BOM, or HTML5 sniffing fallback) and
	// transcodes first -- without this, a legacy-encoded page (Windows-1252
	// etc, not rare even on modern sites) leaks raw non-UTF-8 bytes into
	// title/text, which Postgres's UTF8 column then rejects outright at
	// save time -- a real crawl failure this fixes.
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
	// Defense in depth on top of charset.NewReader: guarantees valid UTF-8
	// no matter what (a wrong encoding guess, a mixed-encoding page), since
	// Postgres's UTF8 column has zero tolerance otherwise. Invalid bytes
	// are replaced with U+FFFD rather than dropped, so a save never loses
	// unrelated content alongside the bad bytes.
	if !utf8.ValidString(title) {
		title = strings.ToValidUTF8(title, "�")
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	return title, text, links, canonicalURL
}
