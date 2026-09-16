package domain

import (
	"net/url"
	"strings"
)

// CanonicalizeURL normalizes rawURL into the exact string form a document's
// identity is derived from (see application.documentID) -- two URLs that
// canonicalize to the same string are the same document, no alias
// bookkeeping needed at all. Always applied, lossless: scheme and host are
// lowercased, a default port matching the scheme (:80 for http, :443 for
// https) is stripped, an empty path becomes "" (so "https://x.example" and
// "https://x.example/" match), and any fragment is dropped -- the same
// fragment-dropping behavior htmlparser.Parse's own link resolution
// already follows. The query string is left untouched: deciding two
// different query strings mean "the same content" is a content-dedup
// concern (see domain.ContentHash/SimHash64), not a URL-identity one.
//
// stripWWW additionally removes a single leading "www." label from the
// host -- gated behind OperationalSettingsValues.URLAliasWWWEnabled, since
// unlike the rest of this function it's a judgment call about site
// structure, not a lossless syntactic normalization (a small number of
// sites genuinely serve different content at www vs. the bare domain).
// When applied, the stripped form becomes the *stored* URL too, so
// www.example.com/x and example.com/x resolve to one identical string --
// and therefore one identical application.documentID -- before any
// document row exists for either, rather than needing a document_aliases
// entry to reconcile two already-separate documents after the fact.
//
// A rawURL that fails to parse is returned unchanged -- callers that need
// a valid URL (host, scheme) have already validated it upstream (see
// application.isCrawlableURL); this function's job is normalization, not
// validation.
func CanonicalizeURL(rawURL string, stripWWW bool) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}

	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if port := u.Port(); port != "" &&
		((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		host = host[:len(host)-len(port)-1]
	}
	if stripWWW {
		host = strings.TrimPrefix(host, "www.")
	}
	u.Host = host
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}
