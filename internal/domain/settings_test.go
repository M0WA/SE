package domain_test

import (
	"testing"
	"time"

	"searchengine/internal/domain"
)

func TestOperationalSettings_NilGetReturnsDefaults(t *testing.T) {
	var s *domain.OperationalSettings
	v := s.Get()
	if v.DefaultTopK != 10 || v.DefaultMaxPages != 20 || v.FetchTimeout != 8*time.Second {
		t.Errorf("expected built-in defaults from nil receiver, got %+v", v)
	}
}

func TestOperationalSettings_NilSetIsNoop(t *testing.T) {
	var s *domain.OperationalSettings
	s.Set(domain.OperationalSettingsValues{DefaultTopK: 99})
}

func TestOperationalSettings_SetAndGetRoundTrip(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{
		FetchTimeout:     3 * time.Second,
		UserAgent:        "custom/1.0",
		DefaultMaxPages:  5,
		MinTextLength:    10,
		DefaultTopK:      3,
		SessionTTL:       2 * time.Hour,
		CrawlDelayMs:     500,
		MaxResponseBytes: 1024,
	})
	v := s.Get()
	if v.FetchTimeout != 3*time.Second || v.UserAgent != "custom/1.0" || v.DefaultMaxPages != 5 ||
		v.MinTextLength != 10 || v.DefaultTopK != 3 || v.SessionTTL != 2*time.Hour ||
		v.CrawlDelayMs != 500 || v.MaxResponseBytes != 1024 {
		t.Errorf("unexpected values after Set, got %+v", v)
	}
}

func TestOperationalSettings_SetSubstitutesDefaultsForZeroValues(t *testing.T) {
	s := domain.NewOperationalSettings(domain.OperationalSettingsValues{})
	v := s.Get()
	d := domain.DefaultOperationalSettings().Get()
	if v.FetchTimeout != d.FetchTimeout || v.UserAgent != d.UserAgent || v.DefaultMaxPages != d.DefaultMaxPages ||
		v.DefaultTopK != d.DefaultTopK || v.SessionTTL != d.SessionTTL || v.MaxResponseBytes != d.MaxResponseBytes {
		t.Errorf("expected zero-valued fields to fall back to defaults, got %+v", v)
	}
	if v.MinTextLength != 0 {
		t.Errorf("expected MinTextLength=0 to be preserved (not defaulted), got %d", v.MinTextLength)
	}
	if v.CrawlDelayMs != 0 {
		t.Errorf("expected CrawlDelayMs=0 to be preserved (not defaulted), got %d", v.CrawlDelayMs)
	}
}

func TestOperationalSettings_SetNegativeMinTextLengthClampsToZero(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MinTextLength: -5})
	if v := s.Get(); v.MinTextLength != 0 {
		t.Errorf("expected negative MinTextLength clamped to 0, got %d", v.MinTextLength)
	}
}

func TestOperationalSettings_SetNegativeCrawlDelayClampsToZero(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{CrawlDelayMs: -5})
	if v := s.Get(); v.CrawlDelayMs != 0 {
		t.Errorf("expected negative CrawlDelayMs clamped to 0, got %d", v.CrawlDelayMs)
	}
}

func TestOperationalSettings_SetNegativeMaxResponseBytesFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxResponseBytes: -5})
	if v := s.Get(); v.MaxResponseBytes != 5*1024*1024 {
		t.Errorf("expected negative MaxResponseBytes to fall back to the default, got %d", v.MaxResponseBytes)
	}
}

func TestOperationalSettings_SetZeroSemanticCandidatePoolSizeFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{SemanticCandidatePoolSize: 0})
	if v := s.Get(); v.SemanticCandidatePoolSize != 200 {
		t.Errorf("expected a zero SemanticCandidatePoolSize to fall back to the default, got %d", v.SemanticCandidatePoolSize)
	}
}

func TestOperationalSettings_SetPositiveSemanticCandidatePoolSizeIsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{SemanticCandidatePoolSize: 500})
	if v := s.Get(); v.SemanticCandidatePoolSize != 500 {
		t.Errorf("expected SemanticCandidatePoolSize=500 to be preserved, got %d", v.SemanticCandidatePoolSize)
	}
}

func TestOperationalSettings_SetZeroDBPoolFieldsFallBackToDefaults(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DBMaxOpenConns: 0, DBMaxIdleConns: 0, DBConnMaxLifetime: 0})
	v := s.Get()
	if v.DBMaxOpenConns != 25 {
		t.Errorf("expected DBMaxOpenConns to fall back to the default 25, got %d", v.DBMaxOpenConns)
	}
	if v.DBMaxIdleConns != 25 {
		t.Errorf("expected DBMaxIdleConns to fall back to the default 25, got %d", v.DBMaxIdleConns)
	}
	if v.DBConnMaxLifetime != 5*time.Minute {
		t.Errorf("expected DBConnMaxLifetime to fall back to the default 5m, got %v", v.DBConnMaxLifetime)
	}
}

func TestOperationalSettings_SetNegativeDBPoolFieldsFallBackToDefaults(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DBMaxOpenConns: -1, DBMaxIdleConns: -1, DBConnMaxLifetime: -1})
	v := s.Get()
	if v.DBMaxOpenConns != 25 || v.DBMaxIdleConns != 25 || v.DBConnMaxLifetime != 5*time.Minute {
		t.Errorf("expected negative DB pool fields to fall back to defaults, got %+v", v)
	}
}

func TestOperationalSettings_SetPositiveDBPoolFieldsArePreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DBMaxOpenConns: 7, DBMaxIdleConns: 4, DBConnMaxLifetime: 90 * time.Second})
	v := s.Get()
	if v.DBMaxOpenConns != 7 || v.DBMaxIdleConns != 4 || v.DBConnMaxLifetime != 90*time.Second {
		t.Errorf("expected configured DB pool fields to be preserved, got %+v", v)
	}
}

func TestOperationalSettings_SetZeroFuzzyMaxEditDistanceFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{FuzzyMaxEditDistance: 0})
	if v := s.Get(); v.FuzzyMaxEditDistance != 2 {
		t.Errorf("expected a zero FuzzyMaxEditDistance to fall back to the default 2, got %d", v.FuzzyMaxEditDistance)
	}
}

func TestOperationalSettings_SetFuzzyMaxEditDistanceClampsAboveTwo(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{FuzzyMaxEditDistance: 9})
	if v := s.Get(); v.FuzzyMaxEditDistance != 2 {
		t.Errorf("expected FuzzyMaxEditDistance clamped to 2, got %d", v.FuzzyMaxEditDistance)
	}
}

func TestOperationalSettings_SetFuzzyMaxEditDistanceOnePreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{FuzzyMaxEditDistance: 1})
	if v := s.Get(); v.FuzzyMaxEditDistance != 1 {
		t.Errorf("expected FuzzyMaxEditDistance=1 to be preserved, got %d", v.FuzzyMaxEditDistance)
	}
}

func TestOperationalSettings_SetFuzzyMatchEnabledFalseIsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{FuzzyMatchEnabled: false, FuzzyMaxEditDistance: 2})
	if v := s.Get(); v.FuzzyMatchEnabled {
		t.Errorf("expected FuzzyMatchEnabled=false to be preserved (not forced back to true), got %+v", v)
	}
}

func TestOperationalSettings_SetZeroPageRankIntervalFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{PageRankRecomputeIntervalMinutes: 0})
	if v := s.Get(); v.PageRankRecomputeIntervalMinutes != 60 {
		t.Errorf("expected a zero PageRankRecomputeIntervalMinutes to fall back to the default 60, got %d", v.PageRankRecomputeIntervalMinutes)
	}
}

func TestOperationalSettings_SetPageRankIntervalClampsToMinimum(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{PageRankRecomputeIntervalMinutes: 1})
	if v := s.Get(); v.PageRankRecomputeIntervalMinutes != 5 {
		t.Errorf("expected PageRankRecomputeIntervalMinutes clamped to the minimum 5, got %d", v.PageRankRecomputeIntervalMinutes)
	}
}

func TestOperationalSettings_SetPageRankIntervalAboveMinimumPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{PageRankRecomputeIntervalMinutes: 120})
	if v := s.Get(); v.PageRankRecomputeIntervalMinutes != 120 {
		t.Errorf("expected PageRankRecomputeIntervalMinutes=120 to be preserved, got %d", v.PageRankRecomputeIntervalMinutes)
	}
}

func TestOperationalSettings_SetANNSearchEnabledFalseIsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ANNSearchEnabled: false})
	if v := s.Get(); v.ANNSearchEnabled {
		t.Errorf("expected ANNSearchEnabled=false to be preserved (not forced back to true), got %+v", v)
	}
}

func TestDefaultOperationalSettings_ReturnsBuiltInDefaults(t *testing.T) {
	v := domain.DefaultOperationalSettings().Get()
	want := domain.OperationalSettingsValues{
		FetchTimeout:                     8 * time.Second,
		UserAgent:                        "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
		DefaultMaxPages:                  20,
		MinTextLength:                    50,
		DefaultTopK:                      10,
		SessionTTL:                       12 * time.Hour,
		CrawlDelayMs:                     250,
		MaxResponseBytes:                 5 * 1024 * 1024,
		SemanticCandidatePoolSize:        200,
		DBMaxOpenConns:                   25,
		DBMaxIdleConns:                   25,
		DBConnMaxLifetime:                5 * time.Minute,
		FuzzyMatchEnabled:                true,
		FuzzyMaxEditDistance:             2,
		PageRankRecomputeIntervalMinutes: 60,
		ANNSearchEnabled:                 true,
		MaxRetainedCrawlJobs:             200,
		DefaultRenderer:                  domain.RendererNone,
		LinkScope:                        domain.LinkScopeDomain,
		MaxDocumentVersions:              5,
		TitleWeight:                      2,
		EmbeddingHashEnabled:             true,
		EmbeddingProvider:                domain.EmbeddingProviderHash,
		EmbeddingHTTPDimensions:          128,
		EmbeddingRateLimitPerSecond:      5,
		EmbeddingTitleWeight:             0.3,
	}
	if v != want {
		t.Errorf("expected defaults %+v, got %+v", want, v)
	}
}

func TestOperationalSettings_SetZeroMaxDocumentVersionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxDocumentVersions: 0})
	if v := s.Get(); v.MaxDocumentVersions != 5 {
		t.Errorf("expected a zero MaxDocumentVersions to fall back to the default 5, got %d", v.MaxDocumentVersions)
	}
}

func TestOperationalSettings_SetNegativeMaxDocumentVersionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxDocumentVersions: -5})
	if v := s.Get(); v.MaxDocumentVersions != 5 {
		t.Errorf("expected a negative MaxDocumentVersions to fall back to the default 5, got %d", v.MaxDocumentVersions)
	}
}

func TestOperationalSettings_SetPositiveMaxDocumentVersionsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxDocumentVersions: 12})
	if v := s.Get(); v.MaxDocumentVersions != 12 {
		t.Errorf("expected MaxDocumentVersions=12 to be preserved, got %d", v.MaxDocumentVersions)
	}
}

func TestOperationalSettings_SetZeroTitleWeightFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{TitleWeight: 0})
	if v := s.Get(); v.TitleWeight != 2 {
		t.Errorf("expected a zero TitleWeight to fall back to the default 2, got %d", v.TitleWeight)
	}
}

func TestOperationalSettings_SetNegativeTitleWeightFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{TitleWeight: -3})
	if v := s.Get(); v.TitleWeight != 2 {
		t.Errorf("expected a negative TitleWeight to fall back to the default 2, got %d", v.TitleWeight)
	}
}

func TestOperationalSettings_SetPositiveTitleWeightPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{TitleWeight: 1})
	if v := s.Get(); v.TitleWeight != 1 {
		t.Errorf("expected TitleWeight=1 (no boost, but still a valid explicit choice) to be preserved, got %d", v.TitleWeight)
	}
	s.Set(domain.OperationalSettingsValues{TitleWeight: 7})
	if v := s.Get(); v.TitleWeight != 7 {
		t.Errorf("expected TitleWeight=7 to be preserved, got %d", v.TitleWeight)
	}
}

func TestOperationalSettings_SetZeroMaxRetainedCrawlJobsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxRetainedCrawlJobs: 0})
	if v := s.Get(); v.MaxRetainedCrawlJobs != 200 {
		t.Errorf("expected a zero MaxRetainedCrawlJobs to fall back to the default 200, got %d", v.MaxRetainedCrawlJobs)
	}
}

func TestOperationalSettings_SetNegativeMaxRetainedCrawlJobsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxRetainedCrawlJobs: -5})
	if v := s.Get(); v.MaxRetainedCrawlJobs != 200 {
		t.Errorf("expected a negative MaxRetainedCrawlJobs to fall back to the default 200, got %d", v.MaxRetainedCrawlJobs)
	}
}

func TestOperationalSettings_SetPositiveMaxRetainedCrawlJobsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxRetainedCrawlJobs: 1000})
	if v := s.Get(); v.MaxRetainedCrawlJobs != 1000 {
		t.Errorf("expected MaxRetainedCrawlJobs=1000 to be preserved, got %d", v.MaxRetainedCrawlJobs)
	}
}

func TestOperationalSettings_SetBlankDefaultRendererFallsBackToNone(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DefaultRenderer: ""})
	if v := s.Get(); v.DefaultRenderer != domain.RendererNone {
		t.Errorf("expected a blank DefaultRenderer to fall back to RendererNone, got %q", v.DefaultRenderer)
	}
}

func TestOperationalSettings_SetInvalidDefaultRendererFallsBackToNone(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DefaultRenderer: "internet-explorer"})
	if v := s.Get(); v.DefaultRenderer != domain.RendererNone {
		t.Errorf("expected an unrecognized DefaultRenderer to fall back to RendererNone, got %q", v.DefaultRenderer)
	}
}

func TestOperationalSettings_SetValidDefaultRendererPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{DefaultRenderer: domain.RendererChromium})
	if v := s.Get(); v.DefaultRenderer != domain.RendererChromium {
		t.Errorf("expected DefaultRenderer=chromium to be preserved, got %q", v.DefaultRenderer)
	}
}

func TestOperationalSettings_SetBlankLinkScopeFallsBackToDomain(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: ""})
	if v := s.Get(); v.LinkScope != domain.LinkScopeDomain {
		t.Errorf("expected a blank LinkScope to fall back to LinkScopeDomain, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetInvalidLinkScopeFallsBackToDomain(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: "planet"})
	if v := s.Get(); v.LinkScope != domain.LinkScopeDomain {
		t.Errorf("expected an unrecognized LinkScope to fall back to LinkScopeDomain, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetValidLinkScopePreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: domain.LinkScopeAny})
	if v := s.Get(); v.LinkScope != domain.LinkScopeAny {
		t.Errorf("expected LinkScope=any to be preserved, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetBlankEmbeddingProviderFallsBackToHash(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingProvider: ""})
	if v := s.Get(); v.EmbeddingProvider != domain.EmbeddingProviderHash {
		t.Errorf("expected a blank EmbeddingProvider to fall back to EmbeddingProviderHash, got %q", v.EmbeddingProvider)
	}
}

func TestOperationalSettings_SetInvalidEmbeddingProviderFallsBackToHash(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingProvider: "ouija-board"})
	if v := s.Get(); v.EmbeddingProvider != domain.EmbeddingProviderHash {
		t.Errorf("expected an unrecognized EmbeddingProvider to fall back to EmbeddingProviderHash, got %q", v.EmbeddingProvider)
	}
}

func TestOperationalSettings_SetValidEmbeddingProviderPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingProvider: domain.EmbeddingProviderHTTP, EmbeddingHTTPEnabled: true})
	if v := s.Get(); v.EmbeddingProvider != domain.EmbeddingProviderHTTP {
		t.Errorf("expected EmbeddingProvider=http to be preserved, got %q", v.EmbeddingProvider)
	}
}

// TestOperationalSettings_SetEmbeddingProviderNamingADisabledProviderSelfHeals
// proves EmbeddingProvider must name an actually-enabled provider --
// naming "http" while EmbeddingHTTPEnabled is left false (the zero value)
// self-heals to "hash" rather than pointing search at a provider with no
// stored vectors to read.
func TestOperationalSettings_SetEmbeddingProviderNamingADisabledProviderSelfHeals(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingProvider: domain.EmbeddingProviderHTTP})
	if v := s.Get(); v.EmbeddingProvider != domain.EmbeddingProviderHash {
		t.Errorf("expected EmbeddingProvider=http (disabled) to self-heal to hash, got %q", v.EmbeddingProvider)
	}
}

// TestOperationalSettings_SetEmbeddingProviderNamingHashWhileOnlyHTTPEnabledSelfHeals
// mirrors the above the other direction: EmbeddingProvider defaults to
// "hash" (the Go zero value's implicit choice isn't actually zero here,
// but an explicit "hash" naming it while only HTTP is enabled) must
// self-heal to the one provider that's actually enabled.
func TestOperationalSettings_SetEmbeddingProviderNamingHashWhileOnlyHTTPEnabledSelfHeals(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingProvider: domain.EmbeddingProviderHash, EmbeddingHashEnabled: false, EmbeddingHTTPEnabled: true})
	if v := s.Get(); v.EmbeddingProvider != domain.EmbeddingProviderHTTP {
		t.Errorf("expected EmbeddingProvider=hash (disabled) to self-heal to http, got %q", v.EmbeddingProvider)
	}
}

func TestOperationalSettings_SetBothEmbeddingProvidersDisabledFallsBackToHashEnabled(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHashEnabled: false, EmbeddingHTTPEnabled: false})
	v := s.Get()
	if !v.EmbeddingHashEnabled {
		t.Errorf("expected both disabled to fall back to EmbeddingHashEnabled=true, got %+v", v)
	}
	if v.EmbeddingProvider != domain.EmbeddingProviderHash {
		t.Errorf("expected EmbeddingProvider to self-heal to hash alongside it, got %q", v.EmbeddingProvider)
	}
}

func TestOperationalSettings_SetBothEmbeddingProvidersEnabledPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHashEnabled: true, EmbeddingHTTPEnabled: true, EmbeddingProvider: domain.EmbeddingProviderHTTP})
	v := s.Get()
	if !v.EmbeddingHashEnabled || !v.EmbeddingHTTPEnabled {
		t.Errorf("expected both providers to stay enabled, got %+v", v)
	}
	if v.EmbeddingProvider != domain.EmbeddingProviderHTTP {
		t.Errorf("expected the explicitly chosen active provider to be preserved when it's enabled, got %q", v.EmbeddingProvider)
	}
}

func TestOperationalSettings_SetZeroEmbeddingHTTPDimensionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPDimensions: 0})
	if v := s.Get(); v.EmbeddingHTTPDimensions != 128 {
		t.Errorf("expected EmbeddingHTTPDimensions=0 to fall back to 128, got %d", v.EmbeddingHTTPDimensions)
	}
}

func TestOperationalSettings_SetNegativeEmbeddingHTTPDimensionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPDimensions: -10})
	if v := s.Get(); v.EmbeddingHTTPDimensions != 128 {
		t.Errorf("expected a negative EmbeddingHTTPDimensions to fall back to 128, got %d", v.EmbeddingHTTPDimensions)
	}
}

func TestOperationalSettings_SetPositiveEmbeddingHTTPDimensionsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPDimensions: 1536})
	if v := s.Get(); v.EmbeddingHTTPDimensions != 1536 {
		t.Errorf("expected EmbeddingHTTPDimensions=1536 to be preserved, got %d", v.EmbeddingHTTPDimensions)
	}
}

func TestOperationalSettings_SetZeroEmbeddingRateLimitFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRateLimitPerSecond: 0})
	if v := s.Get(); v.EmbeddingRateLimitPerSecond != 5 {
		t.Errorf("expected EmbeddingRateLimitPerSecond=0 to fall back to 5, got %d", v.EmbeddingRateLimitPerSecond)
	}
}

func TestOperationalSettings_SetNegativeEmbeddingRateLimitFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRateLimitPerSecond: -10})
	if v := s.Get(); v.EmbeddingRateLimitPerSecond != 5 {
		t.Errorf("expected a negative EmbeddingRateLimitPerSecond to fall back to 5, got %d", v.EmbeddingRateLimitPerSecond)
	}
}

func TestOperationalSettings_SetPositiveEmbeddingRateLimitPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRateLimitPerSecond: 50})
	if v := s.Get(); v.EmbeddingRateLimitPerSecond != 50 {
		t.Errorf("expected EmbeddingRateLimitPerSecond=50 to be preserved, got %d", v.EmbeddingRateLimitPerSecond)
	}
}

func TestOperationalSettings_SetNegativeEmbeddingTitleWeightClampsToZero(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingTitleWeight: -0.5})
	if v := s.Get(); v.EmbeddingTitleWeight != 0 {
		t.Errorf("expected a negative EmbeddingTitleWeight to clamp to 0, got %v", v.EmbeddingTitleWeight)
	}
}

func TestOperationalSettings_SetEmbeddingTitleWeightAboveOneClampsToOne(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingTitleWeight: 1.5})
	if v := s.Get(); v.EmbeddingTitleWeight != 1 {
		t.Errorf("expected EmbeddingTitleWeight=1.5 to clamp to 1, got %v", v.EmbeddingTitleWeight)
	}
}

// TestOperationalSettings_SetZeroEmbeddingTitleWeightPreserved proves 0 is a
// legitimate, meaningful value here (disables title blending entirely --
// see the field's own doc comment) rather than falling back to the
// default the way every other <=0 field does.
func TestOperationalSettings_SetZeroEmbeddingTitleWeightPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingTitleWeight: 0})
	if v := s.Get(); v.EmbeddingTitleWeight != 0 {
		t.Errorf("expected EmbeddingTitleWeight=0 to be preserved, got %v", v.EmbeddingTitleWeight)
	}
}

func TestOperationalSettings_SetInRangeEmbeddingTitleWeightPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingTitleWeight: 0.4})
	if v := s.Get(); v.EmbeddingTitleWeight != 0.4 {
		t.Errorf("expected EmbeddingTitleWeight=0.4 to be preserved, got %v", v.EmbeddingTitleWeight)
	}
}

// TestOperationalSettings_SetBlankEmbeddingHTTPAPIKeyPreservesExisting proves
// the secret-field precedent from the doc comment on OperationalSettings.Set:
// a settings-page save always resubmits every field, including ones the
// admin didn't touch, and the admin form never sees the real stored key (see
// admin.go's toOperationalValues) -- so a blank key in a Set call must never
// wipe out whatever's already configured.
func TestOperationalSettings_SetBlankEmbeddingHTTPAPIKeyPreservesExisting(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: "sk-original"})
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: "", UserAgent: "some-other-change"})
	if v := s.Get(); v.EmbeddingHTTPAPIKey != "sk-original" {
		t.Errorf("expected a blank EmbeddingHTTPAPIKey to preserve the existing key, got %q", v.EmbeddingHTTPAPIKey)
	}
}

func TestOperationalSettings_SetNonBlankEmbeddingHTTPAPIKeyReplacesExisting(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: "sk-original"})
	s.Set(domain.OperationalSettingsValues{EmbeddingHTTPAPIKey: "sk-rotated"})
	if v := s.Get(); v.EmbeddingHTTPAPIKey != "sk-rotated" {
		t.Errorf("expected a non-blank EmbeddingHTTPAPIKey to replace the existing key, got %q", v.EmbeddingHTTPAPIKey)
	}
}

func TestOperationalSettings_SetEmbeddingHTTPBaseURLAndModelPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{
		EmbeddingHTTPBaseURL: "http://localhost:11434/v1",
		EmbeddingHTTPModel:   "nomic-embed-text",
	})
	v := s.Get()
	if v.EmbeddingHTTPBaseURL != "http://localhost:11434/v1" || v.EmbeddingHTTPModel != "nomic-embed-text" {
		t.Errorf("expected EmbeddingHTTPBaseURL/EmbeddingHTTPModel to be preserved, got %+v", v)
	}
}

func TestValidEmbeddingProvider(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{domain.EmbeddingProviderHash, true},
		{domain.EmbeddingProviderHTTP, true},
		{"", false},
		{"ouija-board", false},
	}
	for _, tc := range cases {
		if got := domain.ValidEmbeddingProvider(tc.name); got != tc.want {
			t.Errorf("ValidEmbeddingProvider(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidLinkScope(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{domain.LinkScopeDefault, true},
		{domain.LinkScopeHost, true},
		{domain.LinkScopeDomain, true},
		{domain.LinkScopeTLD, true},
		{domain.LinkScopeAny, true},
		{"planet", false},
	}
	for _, tc := range cases {
		if got := domain.ValidLinkScope(tc.name); got != tc.want {
			t.Errorf("ValidLinkScope(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestValidRenderer(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{domain.RendererDefault, true},
		{domain.RendererNone, true},
		{domain.RendererChromium, true},
		{domain.RendererFirefox, true},
		{"internet-explorer", false},
	}
	for _, tc := range cases {
		if got := domain.ValidRenderer(tc.name); got != tc.want {
			t.Errorf("ValidRenderer(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
