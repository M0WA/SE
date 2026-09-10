package domain

import (
	"regexp"
	"strings"
)

var queryTokenRe = regexp.MustCompile(`"[^"]*"|\S+`)

// ParsedQuery is a search query broken into its structural pieces: plain
// optional terms that just contribute to relevance ranking, required terms
// (+word) and excluded terms (-word), and required exact phrases ("quoted
// text").
type ParsedQuery struct {
	Optional []string
	Required []string
	Excluded []string
	Phrases  []string
}

// ParseQuery splits a raw query string into its structural pieces. It must
// run on the raw string before Tokenize, which would otherwise strip the
// +/-/" syntax characters this depends on.
func ParseQuery(raw string) ParsedQuery {
	var parsed ParsedQuery
	for _, tok := range queryTokenRe.FindAllString(raw, -1) {
		switch {
		case len(tok) >= 2 && strings.HasPrefix(tok, `"`) && strings.HasSuffix(tok, `"`):
			phrase := strings.ToLower(strings.TrimSpace(tok[1 : len(tok)-1]))
			if phrase != "" {
				parsed.Phrases = append(parsed.Phrases, phrase)
			}
		case strings.HasPrefix(tok, "+") && len(tok) > 1:
			parsed.Required = append(parsed.Required, Tokenize(tok[1:])...)
		case strings.HasPrefix(tok, "-") && len(tok) > 1:
			parsed.Excluded = append(parsed.Excluded, Tokenize(tok[1:])...)
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
// terms or phrases that need per-document filtering, beyond ordinary
// relevance ranking.
func (q ParsedQuery) HasConstraints() bool {
	return len(q.Required) > 0 || len(q.Excluded) > 0 || len(q.Phrases) > 0
}

// Matches reports whether a document's title and text satisfy this query's
// required words, excluded words, and required phrases. Required/excluded
// checks use the same tokenization as indexing, for consistency with what
// the BM25 postings actually contain. Phrase checks are a literal,
// case-insensitive substring match, since phrases aren't positionally
// indexed.
func (q ParsedQuery) Matches(title, text string) bool {
	if !q.HasConstraints() {
		return true
	}

	if len(q.Required) > 0 || len(q.Excluded) > 0 {
		docTokens := Tokenize(title + " " + text)
		tokenSet := make(map[string]bool, len(docTokens))
		for _, t := range docTokens {
			tokenSet[t] = true
		}
		for _, req := range q.Required {
			if !tokenSet[req] {
				return false
			}
		}
		for _, exc := range q.Excluded {
			if tokenSet[exc] {
				return false
			}
		}
	}

	if len(q.Phrases) > 0 {
		haystack := strings.ToLower(title + " " + text)
		for _, phrase := range q.Phrases {
			if !strings.Contains(haystack, phrase) {
				return false
			}
		}
	}
	return true
}
