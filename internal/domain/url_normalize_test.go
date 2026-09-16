package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestCanonicalizeURL_LowercasesSchemeAndHost(t *testing.T) {
	got := domain.CanonicalizeURL("HTTPS://Example.COM/Path", false)
	if got != "https://example.com/Path" {
		t.Errorf("expected lowercased scheme/host with path case preserved, got %q", got)
	}
}

func TestCanonicalizeURL_StripsDefaultPort(t *testing.T) {
	cases := map[string]string{
		"http://example.com:80/x":   "http://example.com/x",
		"https://example.com:443/x": "https://example.com/x",
		"http://example.com:8080/x": "http://example.com:8080/x",
		"https://example.com:80/x":  "https://example.com:80/x",
	}
	for in, want := range cases {
		if got := domain.CanonicalizeURL(in, false); got != want {
			t.Errorf("CanonicalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalizeURL_EmptyPathBecomesBare(t *testing.T) {
	got := domain.CanonicalizeURL("https://example.com/", false)
	if got != "https://example.com" {
		t.Errorf("expected the bare-root form, got %q", got)
	}
}

func TestCanonicalizeURL_DropsFragment(t *testing.T) {
	got := domain.CanonicalizeURL("https://example.com/x#section", false)
	if got != "https://example.com/x" {
		t.Errorf("expected fragment dropped, got %q", got)
	}
}

func TestCanonicalizeURL_LeavesQueryStringUntouched(t *testing.T) {
	got := domain.CanonicalizeURL("https://example.com/x?utm_source=y", false)
	if got != "https://example.com/x?utm_source=y" {
		t.Errorf("expected query string preserved, got %q", got)
	}
}

func TestCanonicalizeURL_StripWWWFalseKeepsHostAsIs(t *testing.T) {
	got := domain.CanonicalizeURL("https://www.example.com/x", false)
	if got != "https://www.example.com/x" {
		t.Errorf("expected www kept when stripWWW is false, got %q", got)
	}
}

func TestCanonicalizeURL_StripWWWTrueRemovesWWWPrefix(t *testing.T) {
	got := domain.CanonicalizeURL("https://www.example.com/x", true)
	if got != "https://example.com/x" {
		t.Errorf("expected www stripped, got %q", got)
	}
}

// The whole point of stripWWW: both forms of the same page must
// canonicalize to the byte-identical string, so they hash to the same
// application.documentID with no alias bookkeeping needed.
func TestCanonicalizeURL_WWWAndBareHostConverge(t *testing.T) {
	www := domain.CanonicalizeURL("https://www.example.com/x", true)
	bare := domain.CanonicalizeURL("https://example.com/x", true)
	if www != bare {
		t.Errorf("expected www and bare host to converge, got %q vs %q", www, bare)
	}
}

func TestCanonicalizeURL_StripWWWOnlyStripsLeadingLabel(t *testing.T) {
	got := domain.CanonicalizeURL("https://www.sub.example.com/x", true)
	if got != "https://sub.example.com/x" {
		t.Errorf("expected only the leading www. label stripped, got %q", got)
	}
}

func TestCanonicalizeURL_MalformedURLReturnedUnchanged(t *testing.T) {
	raw := "http://[::invalid"
	if got := domain.CanonicalizeURL(raw, true); got != raw {
		t.Errorf("expected a malformed URL returned unchanged, got %q", got)
	}
}

func TestCanonicalizeURL_NoHostReturnedUnchanged(t *testing.T) {
	raw := "not-a-url"
	if got := domain.CanonicalizeURL(raw, true); got != raw {
		t.Errorf("expected a hostless string returned unchanged, got %q", got)
	}
}
