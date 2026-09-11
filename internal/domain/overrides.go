package domain

import (
	"net/url"
	"strings"
	"sync"
)

// RankingOverridesValues is a snapshot of every admin-configured ranking
// override: specific words or domains blocked from results entirely, and
// specific words or domains whose matching documents get a final-score
// multiplier. Independent of TuningSettings' alpha/k1/b knobs (see that
// type) -- these act after the BM25/semantic blend, not on it.
type RankingOverridesValues struct {
	BlockedTerms   []string
	BoostedTerms   map[string]float64
	BlockedDomains []string
	BoostedDomains map[string]float64
}

// Blocked reports whether doc should be excluded from results entirely:
// its URL's host is in BlockedDomains, or its title/text contains a
// BlockedTerm.
func (v RankingOverridesValues) Blocked(doc Document) bool {
	if len(v.BlockedDomains) > 0 {
		host := hostOf(doc.URL)
		for _, d := range v.BlockedDomains {
			if d == host {
				return true
			}
		}
	}
	if len(v.BlockedTerms) == 0 {
		return false
	}
	tokens := docTokenSet(doc)
	for _, term := range v.BlockedTerms {
		if tokens[term] {
			return true
		}
	}
	return false
}

// BoostFactor returns the multiplier to apply to doc's final score: 1.0
// (no-op) if nothing matches, otherwise the product of every matching
// BoostedDomains/BoostedTerms factor.
func (v RankingOverridesValues) BoostFactor(doc Document) float64 {
	factor := 1.0
	if len(v.BoostedDomains) > 0 {
		if f, ok := v.BoostedDomains[hostOf(doc.URL)]; ok {
			factor *= f
		}
	}
	if len(v.BoostedTerms) > 0 {
		tokens := docTokenSet(doc)
		for term, f := range v.BoostedTerms {
			if tokens[term] {
				factor *= f
			}
		}
	}
	return factor
}

func docTokenSet(doc Document) map[string]bool {
	tokens := Tokenize(doc.Title + " " + doc.Text)
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

func hostOf(rawURL string) string {
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

// normalizeTerms tokenizes every raw entry the same way document text is
// tokenized (Tokenize), so a blocked/boosted word matches a document
// regardless of case or punctuation, and dedupes the result. A multi-word
// entry ("New York") expands into each of its tokens, matching if any one
// of them appears in a document -- consistent with how a plain query word
// is tokenized before it's looked up.
func normalizeTerms(raw []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range raw {
		for _, t := range Tokenize(r) {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// normalizeTermFactors is normalizeTerms for a term->factor map. Entries
// with a non-positive factor are dropped -- a boost of zero or less isn't
// a boost, and silently omitting it is simpler than rejecting the whole
// Set call, consistent with how OperationalSettings substitutes rather
// than validates.
func normalizeTermFactors(raw map[string]float64) map[string]float64 {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]float64, len(raw))
	for term, factor := range raw {
		if factor <= 0 {
			continue
		}
		for _, t := range Tokenize(term) {
			out[t] = factor
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeDomain lowercases a domain entry, accepting a full URL (e.g.
// pasted from a browser bar) by extracting its host -- convenient since
// the admin UI's field is documented as accepting domains or URLs.
func normalizeDomain(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return strings.ToLower(u.Hostname())
		}
	}
	return raw
}

func normalizeDomains(raw []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range raw {
		d := normalizeDomain(r)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func normalizeDomainFactors(raw map[string]float64) map[string]float64 {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]float64, len(raw))
	for host, factor := range raw {
		if factor <= 0 {
			continue
		}
		d := normalizeDomain(host)
		if d == "" {
			continue
		}
		out[d] = factor
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// Get returns the current values. A nil *RankingOverrides (e.g. a search
// path that hasn't wired overrides in) returns the empty zero value --
// nothing blocked or boosted -- rather than a zero-value struct, so
// callers never need a separate nil check before reading.
func (s *RankingOverrides) Get() RankingOverridesValues {
	if s == nil {
		return RankingOverridesValues{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v.clone()
}

// Set updates the overrides, normalizing every field (tokenizing words,
// lowercasing/parsing domains, dropping non-positive boost factors)
// rather than rejecting the update -- this is an admin convenience knob,
// not a user-facing form that needs field-level validation errors.
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
