package domain

import (
	"net/url"
	"strings"
	"sync"
)

// RankingOverridesValues is a snapshot of every admin-configured ranking
// override: words/domains blocked entirely, and words/domains whose
// matches get a final-score multiplier. Acts after the BM25/semantic
// blend, independent of TuningSettings' alpha/k1/b knobs.
type RankingOverridesValues struct {
	BlockedTerms   []string
	BoostedTerms   map[string]float64
	BlockedDomains []string
	BoostedDomains map[string]float64
}

// Blocked reports whether doc should be excluded entirely: its host is in
// BlockedDomains, or its title/text contains a BlockedTerm.
func (v RankingOverridesValues) Blocked(doc Document) bool {
	return v.BlockedTokens(doc.URL, TokenSet(doc.Title, doc.Text))
}

// BlockedTokens is Blocked's counterpart for a caller that already
// tokenized the document (see TokenSet) for some other reason.
func (v RankingOverridesValues) BlockedTokens(url string, tokens map[string]bool) bool {
	if len(v.BlockedDomains) > 0 {
		host := HostOf(url)
		for _, d := range v.BlockedDomains {
			if d == host {
				return true
			}
		}
	}
	for _, term := range v.BlockedTerms {
		if tokens[term] {
			return true
		}
	}
	return false
}

// BoostFactor returns the multiplier for doc's final score: 1.0 if nothing
// matches, otherwise the product of every matching Boosted*/Terms factor.
func (v RankingOverridesValues) BoostFactor(doc Document) float64 {
	return v.BoostFactorTokens(doc.URL, TokenSet(doc.Title, doc.Text))
}

// BoostFactorTokens is BoostFactor's counterpart for a caller that already
// tokenized the document (see TokenSet) for some other reason.
func (v RankingOverridesValues) BoostFactorTokens(url string, tokens map[string]bool) float64 {
	factor := 1.0
	if len(v.BoostedDomains) > 0 {
		if f, ok := v.BoostedDomains[HostOf(url)]; ok {
			factor *= f
		}
	}
	for term, f := range v.BoostedTerms {
		if tokens[term] {
			factor *= f
		}
	}
	return factor
}

// TokenSet tokenizes title+text into a set for O(1) membership checks --
// shared by Blocked/BoostFactor and ParsedQuery.Matches.
func TokenSet(title, text string) map[string]bool {
	tokens := Tokenize(title + " " + text)
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

// HostOf extracts the lowercased hostname from a URL, or "" if it doesn't
// parse -- shared by ranking overrides, site: filters, and crawl dedup.
func HostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (v RankingOverridesValues) clone() RankingOverridesValues {
	out := RankingOverridesValues{}
	if len(v.BlockedTerms) > 0 {
		out.BlockedTerms = append([]string(nil), v.BlockedTerms...)
	}
	if len(v.BlockedDomains) > 0 {
		out.BlockedDomains = append([]string(nil), v.BlockedDomains...)
	}
	if len(v.BoostedTerms) > 0 {
		out.BoostedTerms = make(map[string]float64, len(v.BoostedTerms))
		for k, f := range v.BoostedTerms {
			out.BoostedTerms[k] = f
		}
	}
	if len(v.BoostedDomains) > 0 {
		out.BoostedDomains = make(map[string]float64, len(v.BoostedDomains))
		for k, f := range v.BoostedDomains {
			out.BoostedDomains[k] = f
		}
	}
	return out
}

// appendIfNew appends item unless seen already has it -- the ordered-dedupe
// idiom shared below.
func appendIfNew(out []string, seen map[string]bool, item string) []string {
	if seen[item] {
		return out
	}
	seen[item] = true
	return append(out, item)
}

// normalizeTerms tokenizes every raw entry like document text, so matching
// is case/punctuation-insensitive, and dedupes. A multi-word entry ("New
// York") expands into each token.
func normalizeTerms(raw []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range raw {
		for _, t := range Tokenize(r) {
			out = appendIfNew(out, seen, t)
		}
	}
	return out
}

// normalizeFactors is shared by normalizeTermFactors/normalizeDomainFactors:
// entries with a non-positive factor are dropped rather than rejecting the
// whole Set call. keys maps one raw key to its normalized key(s).
func normalizeFactors(raw map[string]float64, keys func(string) []string) map[string]float64 {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]float64, len(raw))
	for k, factor := range raw {
		if factor <= 0 {
			continue
		}
		for _, key := range keys(k) {
			out[key] = factor
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeTermFactors is normalizeTerms for a term->factor map.
func normalizeTermFactors(raw map[string]float64) map[string]float64 {
	return normalizeFactors(raw, Tokenize)
}

// normalizeDomain lowercases a domain entry, accepting a full URL by
// extracting its host -- the admin UI field accepts either.
func normalizeDomain(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if h := HostOf(raw); h != "" {
			return h
		}
	}
	return raw
}

func normalizeDomains(raw []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range raw {
		if d := normalizeDomain(r); d != "" {
			out = appendIfNew(out, seen, d)
		}
	}
	return out
}

func normalizeDomainFactors(raw map[string]float64) map[string]float64 {
	return normalizeFactors(raw, func(host string) []string {
		if d := normalizeDomain(host); d != "" {
			return []string{d}
		}
		return nil
	})
}

// RankingOverrides holds the overrides, safe for concurrent use: read on
// every search, written from the admin overrides panel.
type RankingOverrides struct {
	mu sync.RWMutex
	v  RankingOverridesValues
}

func NewRankingOverrides(v RankingOverridesValues) *RankingOverrides {
	s := &RankingOverrides{}
	s.Set(v)
	return s
}

// DefaultRankingOverrides constructs overrides with nothing blocked or
// boosted, for callers that don't need to override anything at startup.
func DefaultRankingOverrides() *RankingOverrides {
	return NewRankingOverrides(RankingOverridesValues{})
}

// Get returns the current values. A nil *RankingOverrides returns the
// empty zero value, so callers never need a separate nil check.
func (s *RankingOverrides) Get() RankingOverridesValues {
	if s == nil {
		return RankingOverridesValues{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v.clone()
}

// Set updates the overrides, normalizing every field rather than rejecting
// the update -- an admin convenience knob, not a validated form.
func (s *RankingOverrides) Set(v RankingOverridesValues) {
	if s == nil {
		return
	}
	normalized := RankingOverridesValues{
		BlockedTerms:   normalizeTerms(v.BlockedTerms),
		BoostedTerms:   normalizeTermFactors(v.BoostedTerms),
		BlockedDomains: normalizeDomains(v.BlockedDomains),
		BoostedDomains: normalizeDomainFactors(v.BoostedDomains),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = normalized
}
