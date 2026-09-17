package domain

import (
	"regexp"
	"strings"
)

// The optional leading [+-] lets a sign attach directly to a quoted phrase
// as one token (`-"exact phrase"`) -- without it, the plain \S+ alternative
// would split that into two nonsensical tokens (`-"exact`, `phrase"`).
var queryTokenRe = regexp.MustCompile(`[+-]?"[^"]*"|\S+`)

// sitePrefix is the "site:" operator prefix, matched case-insensitively
// (e.g. "Site:Example.com" is equivalent to "site:example.com").
const sitePrefix = "site:"

// ParsedQuery is a search query broken into its structural pieces: plain
// optional terms that just contribute to relevance ranking, required terms
// (+word) and excluded terms (-word), required exact phrases ("quoted
// text") and excluded ones (-"quoted text"), and site: filters restricting
// results to (site:host) or away from (-site:host) one or more hosts.
type ParsedQuery struct {
	Optional        []string
	Required        []string
	Excluded        []string
	Phrases         []string
	ExcludedPhrases []string
	Sites           []string
	ExcludedSites   []string
}

// ParseQuery splits a raw query string into its structural pieces. Must run
// on the raw string before Tokenize, which would strip the +/-/"/site:
// syntax this depends on. Each token's leading +/- is stripped once into
// `sign`, which the phrase/site: cases below key off directly rather than
// re-deriving from `tok`.
func ParseQuery(raw string) ParsedQuery {
	var parsed ParsedQuery
	for _, tok := range queryTokenRe.FindAllString(raw, -1) {
		var sign byte
		body := tok
		if len(tok) > 0 && (tok[0] == '+' || tok[0] == '-') {
			sign = tok[0]
			body = tok[1:]
		}
		switch {
		case len(body) >= 2 && strings.HasPrefix(body, `"`) && strings.HasSuffix(body, `"`):
			phrase := strings.ToLower(strings.TrimSpace(body[1 : len(body)-1]))
			if phrase != "" {
				if sign == '-' {
					parsed.ExcludedPhrases = append(parsed.ExcludedPhrases, phrase)
				} else {
					parsed.Phrases = append(parsed.Phrases, phrase)
				}
			}
		case len(body) >= len(sitePrefix) && strings.EqualFold(body[:len(sitePrefix)], sitePrefix):
			site := strings.ToLower(strings.TrimSpace(body[len(sitePrefix):]))
			if site != "" {
				if sign == '-' {
					parsed.ExcludedSites = append(parsed.ExcludedSites, site)
				} else {
					parsed.Sites = append(parsed.Sites, site)
				}
			}
		case sign == '+' && len(body) > 0:
			parsed.Required = append(parsed.Required, Tokenize(body)...)
		case sign == '-' && len(body) > 0:
			parsed.Excluded = append(parsed.Excluded, Tokenize(body)...)
		default:
			parsed.Optional = append(parsed.Optional, Tokenize(tok)...)
		}
	}
	return parsed
}

// AllTerms returns every word across optional, required, and phrase terms,
// deduplicated -- for BM25 postings lookups and the semantic query
// embedding. Excluded terms are never included: they should never boost
// relevance or get highlighted.
func (q ParsedQuery) AllTerms() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(words []string) {
		for _, w := range words {
			if !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
	}
	add(q.Optional)
	add(q.Required)
	for _, phrase := range q.Phrases {
		add(Tokenize(phrase))
	}
	return out
}

// Empty reports whether the query has no usable terms at all.
func (q ParsedQuery) Empty() bool {
	return len(q.AllTerms()) == 0
}

// HasConstraints reports whether this query has any required/excluded
// terms, phrases, or site: filters that need per-document filtering,
// beyond ordinary relevance ranking.
func (q ParsedQuery) HasConstraints() bool {
	return len(q.Required) > 0 || len(q.Excluded) > 0 || len(q.Phrases) > 0 ||
		len(q.ExcludedPhrases) > 0 || len(q.Sites) > 0 || len(q.ExcludedSites) > 0
}

// SiteAllowed reports whether doc's host satisfies this query's site:
// filter(s). -site:host always wins over a positive site: filter. Matches
// an exact host or any subdomain ("site:example.com" also matches
// "www.example.com"). No filters at all allows every host.
func (q ParsedQuery) SiteAllowed(doc Document) bool {
	host := HostOf(doc.URL)
	for _, site := range q.ExcludedSites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return false
		}
	}
	if len(q.Sites) == 0 {
		return true
	}
	for _, site := range q.Sites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return true
		}
	}
	return false
}

// Matches reports whether title/text satisfy this query's required/excluded
// words and phrases -- a convenience wrapper around MatchesTokens for a
// caller without pre-tokenized text (hybrid_search_service.go calls
// MatchesTokens directly since it reuses those tokens for ranking too).
func (q ParsedQuery) Matches(title, text string) bool {
	if !q.HasConstraints() {
		return true
	}
	var tokens map[string]bool
	if len(q.Required) > 0 || len(q.Excluded) > 0 {
		tokens = TokenSet(title, text)
	}
	return q.MatchesTokens(tokens, title, text)
}

// MatchesTokens is Matches' counterpart for a caller that already
// tokenized title+text. tokens is only consulted when required/excluded
// words exist -- pass nil for a query known to be phrase-only. Phrase
// checks are a literal, case-insensitive substring match (phrases aren't
// positionally indexed).
func (q ParsedQuery) MatchesTokens(tokens map[string]bool, title, text string) bool {
	if !q.HasConstraints() {
		return true
	}

	if len(q.Required) > 0 || len(q.Excluded) > 0 {
		for _, req := range q.Required {
			if !tokens[req] {
				return false
			}
		}
		for _, exc := range q.Excluded {
			if tokens[exc] {
				return false
			}
		}
	}

	if len(q.Phrases) > 0 || len(q.ExcludedPhrases) > 0 {
		haystack := strings.ToLower(title + " " + text)
		for _, phrase := range q.Phrases {
			if !strings.Contains(haystack, phrase) {
				return false
			}
		}
		for _, phrase := range q.ExcludedPhrases {
			if strings.Contains(haystack, phrase) {
				return false
			}
		}
	}
	return true
}
