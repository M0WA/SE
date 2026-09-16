package htmlparser

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Parse extracts a page's title, visible text, outbound links, and its
// declared canonical URL (from <link rel="canonical" href="...">, if
// present) -- see application.crawlLoop, which skips ever creating a
// document row for a page whose canonical points elsewhere. canonicalURL
// is "" when no such tag exists, resolved the same way <a href> links are
// (relative to pageURL, fragment dropped), and only kept if it resolves to
// an http/https URL.
func Parse(r io.Reader, pageURL string) (title, text string, links []string, canonicalURL string) {
	doc, err := html.Parse(r)
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
						if u, err := url.Parse(a.Val); err == nil && base != nil {
							abs := base.ResolveReference(u)
							abs.Fragment = ""
							if abs.Scheme == "http" || abs.Scheme == "https" {
								links = append(links, abs.String())
							}
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
					if u, err := url.Parse(href); err == nil && base != nil {
						abs := base.ResolveReference(u)
						abs.Fragment = ""
						if abs.Scheme == "http" || abs.Scheme == "https" {
							canonicalURL = abs.String()
						}
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
	return title, text, links, canonicalURL
}
