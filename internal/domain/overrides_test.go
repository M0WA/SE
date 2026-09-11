package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestRankingOverrides_NilGetReturnsEmptyDefaults(t *testing.T) {
	var s *domain.RankingOverrides
	v := s.Get()
	if len(v.BlockedTerms) != 0 || len(v.BoostedTerms) != 0 || len(v.BlockedDomains) != 0 || len(v.BoostedDomains) != 0 {
		t.Errorf("expected empty overrides from nil receiver, got %+v", v)
	}
}

func TestRankingOverrides_NilSetIsNoop(t *testing.T) {
	var s *domain.RankingOverrides
	s.Set(domain.RankingOverridesValues{BlockedTerms: []string{"spam"}})
}

func TestRankingOverrides_SetAndGetRoundTrip(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{
		BlockedTerms:   []string{"Spam"},
		BoostedTerms:   map[string]float64{"Official": 1.5},
		BlockedDomains: []string{"Spammy.example"},
		BoostedDomains: map[string]float64{"Trusted.example": 2.0},
	})
	v := s.Get()
	if len(v.BlockedTerms) != 1 || v.BlockedTerms[0] != "spam" {
		t.Errorf("expected blocked term normalized to lowercase, got %+v", v.BlockedTerms)
	}
	if v.BoostedTerms["official"] != 1.5 {
		t.Errorf("expected boosted term normalized to lowercase key, got %+v", v.BoostedTerms)
	}
	if len(v.BlockedDomains) != 1 || v.BlockedDomains[0] != "spammy.example" {
		t.Errorf("expected blocked domain lowercased, got %+v", v.BlockedDomains)
	}
	if v.BoostedDomains["trusted.example"] != 2.0 {
		t.Errorf("expected boosted domain lowercased key, got %+v", v.BoostedDomains)
	}
}

func TestRankingOverrides_GetReturnsIndependentCopy(t *testing.T) {
	s := domain.NewRankingOverrides(domain.RankingOverridesValues{BlockedTerms: []string{"spam"}})
	v := s.Get()
	v.BlockedTerms[0] = "mutated"
	if got := s.Get().BlockedTerms[0]; got != "spam" {
		t.Errorf("expected internal state unaffected by mutating a Get() result, got %q", got)
	}
}

func TestRankingOverrides_SetAcceptsURLAsDomain(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{BlockedDomains: []string{"https://Spammy.example/some/path?x=1"}})
	v := s.Get()
	if len(v.BlockedDomains) != 1 || v.BlockedDomains[0] != "spammy.example" {
		t.Errorf("expected host extracted from URL and lowercased, got %+v", v.BlockedDomains)
	}
}

func TestRankingOverrides_SetDropsNonPositiveBoostFactors(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{
		BoostedTerms:   map[string]float64{"good": 1.5, "zero": 0, "negative": -1},
		BoostedDomains: map[string]float64{"good.example": 2.0, "zero.example": 0},
	})
	v := s.Get()
	if len(v.BoostedTerms) != 1 || v.BoostedTerms["good"] != 1.5 {
		t.Errorf("expected only the positive-factor term kept, got %+v", v.BoostedTerms)
	}
	if len(v.BoostedDomains) != 1 || v.BoostedDomains["good.example"] != 2.0 {
		t.Errorf("expected only the positive-factor domain kept, got %+v", v.BoostedDomains)
	}
}

func TestRankingOverridesValues_Blocked_ByDomain(t *testing.T) {
	v := domain.RankingOverridesValues{BlockedDomains: []string{"spammy.example"}}
	blocked := domain.Document{URL: "https://spammy.example/page", Title: "T", Text: "hello"}
	allowed := domain.Document{URL: "https://ok.example/page", Title: "T", Text: "hello"}
	if !v.Blocked(blocked) {
		t.Error("expected document on a blocked domain to be blocked")
	}
	if v.Blocked(allowed) {
		t.Error("expected document on an unrelated domain to not be blocked")
	}
}

func TestRankingOverridesValues_Blocked_ByTerm(t *testing.T) {
	v := domain.RankingOverridesValues{BlockedTerms: []string{"casino"}}
	blocked := domain.Document{URL: "https://ok.example", Title: "Win big", Text: "Visit our casino today"}
	allowed := domain.Document{URL: "https://ok.example", Title: "Cats", Text: "Cats are great pets"}
	if !v.Blocked(blocked) {
		t.Error("expected document containing a blocked word to be blocked")
	}
	if v.Blocked(allowed) {
		t.Error("expected document without a blocked word to not be blocked")
	}
}

func TestRankingOverridesValues_BoostFactor_DefaultsToOne(t *testing.T) {
	v := domain.RankingOverridesValues{}
	if f := v.BoostFactor(domain.Document{URL: "https://ok.example", Title: "T", Text: "text"}); f != 1.0 {
		t.Errorf("expected default boost factor 1.0, got %v", f)
	}
}

func TestRankingOverridesValues_Blocked_MalformedURLTreatedAsNoHost(t *testing.T) {
	v := domain.RankingOverridesValues{BlockedDomains: []string{"spammy.example"}}
	doc := domain.Document{URL: "http://spammy.example/\n", Title: "T", Text: "hello"}
	if v.Blocked(doc) {
		t.Error("expected a document whose URL fails to parse to not match any blocked domain")
	}
}

func TestRankingOverrides_SetDedupesEquivalentDomains(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{BlockedDomains: []string{"Spammy.example", "spammy.example", "  spammy.example  "}})
	v := s.Get()
	if len(v.BlockedDomains) != 1 {
		t.Errorf("expected duplicate domains (after normalizing) to collapse to one entry, got %+v", v.BlockedDomains)
	}
}

func TestRankingOverrides_SetSkipsBlankDomainEntries(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{BlockedDomains: []string{"", "   "}})
	v := s.Get()
	if len(v.BlockedDomains) != 0 {
		t.Errorf("expected blank domain entries to be dropped, got %+v", v.BlockedDomains)
	}
}

func TestRankingOverrides_SetURLDomainWithNoHostKeptAsIs(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{BlockedDomains: []string{"http://"}})
	v := s.Get()
	if len(v.BlockedDomains) != 1 || v.BlockedDomains[0] != "http://" {
		t.Errorf("expected a host-less URL to fall back to the raw (lowercased) string, got %+v", v.BlockedDomains)
	}
}

func TestRankingOverrides_SetAllNonPositiveBoostsLeaveMapNil(t *testing.T) {
	s := domain.DefaultRankingOverrides()
	s.Set(domain.RankingOverridesValues{
		BoostedTerms:   map[string]float64{"zero": 0},
		BoostedDomains: map[string]float64{" ": 2.0},
	})
	v := s.Get()
	if len(v.BoostedTerms) != 0 {
		t.Errorf("expected no boosted terms to survive an all-non-positive input, got %+v", v.BoostedTerms)
	}
	if len(v.BoostedDomains) != 0 {
		t.Errorf("expected a blank domain key to be dropped even with a positive factor, got %+v", v.BoostedDomains)
	}
}

func TestRankingOverridesValues_BoostFactor_CombinesTermAndDomain(t *testing.T) {
	v := domain.RankingOverridesValues{
		BoostedDomains: map[string]float64{"trusted.example": 2.0},
		BoostedTerms:   map[string]float64{"official": 1.5},
	}
	doc := domain.Document{URL: "https://trusted.example/page", Title: "Official notice", Text: "content"}
	if f := v.BoostFactor(doc); f != 3.0 {
		t.Errorf("expected domain and term boosts to multiply to 3.0, got %v", f)
	}
}
