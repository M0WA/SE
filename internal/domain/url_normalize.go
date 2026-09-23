package domain

import (
	"net/url"
	"strings"
)

// CanonicalizeURL normalizes rawURL into the string a document's identity
// is derived from: scheme/host lowercased, default port and fragment
// stripped, empty path. Query string untouched. stripWWW also strips a
// leading "www." (gated behind URLAliasWWWEnabled -- lossy, a judgment call).
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
