package domain_test

import (
	"reflect"
	"testing"
	"time"

	"searchengine/internal/domain"
)

func TestOperationalSettings_NilGetReturnsDefaults(t *testing.T) {
	var s *domain.OperationalSettings
	v := s.Get()
	if v.DefaultTopK != 5000 || v.DefaultMaxPages != 20000 || v.FetchTimeout != 8*time.Second {
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

func TestOperationalSettings_SetZeroEmbeddingRecomputeConcurrencyFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRecomputeConcurrency: 0})
	if v := s.Get(); v.EmbeddingRecomputeConcurrency != 4 {
		t.Errorf("expected a zero EmbeddingRecomputeConcurrency to fall back to the default, got %d", v.EmbeddingRecomputeConcurrency)
	}
}

func TestOperationalSettings_SetNegativeEmbeddingRecomputeConcurrencyFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRecomputeConcurrency: -1})
	if v := s.Get(); v.EmbeddingRecomputeConcurrency != 4 {
		t.Errorf("expected a negative EmbeddingRecomputeConcurrency to fall back to the default, got %d", v.EmbeddingRecomputeConcurrency)
	}
}

func TestOperationalSettings_SetPositiveEmbeddingRecomputeConcurrencyIsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingRecomputeConcurrency: 16})
	if v := s.Get(); v.EmbeddingRecomputeConcurrency != 16 {
		t.Errorf("expected EmbeddingRecomputeConcurrency=16 to be preserved, got %d", v.EmbeddingRecomputeConcurrency)
	}
}

func TestOperationalSettings_SetZeroSemanticRescoreCapFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{SemanticRescoreCap: 0})
	if v := s.Get(); v.SemanticRescoreCap != 500 {
		t.Errorf("expected a zero SemanticRescoreCap to fall back to the default, got %d", v.SemanticRescoreCap)
	}
}

func TestOperationalSettings_SetPositiveSemanticRescoreCapIsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{SemanticRescoreCap: 1000})
	if v := s.Get(); v.SemanticRescoreCap != 1000 {
		t.Errorf("expected SemanticRescoreCap=1000 to be preserved, got %d", v.SemanticRescoreCap)
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
		DefaultMaxPages:                  20000,
		MinTextLength:                    50,
		DefaultTopK:                      5000,
		SessionTTL:                       12 * time.Hour,
		CrawlDelayMs:                     250,
		MaxResponseBytes:                 5 * 1024 * 1024,
		SemanticCandidatePoolSize:        200,
		SemanticRescoreCap:               500,
		DBMaxOpenConns:                   25,
		DBMaxIdleConns:                   25,
		DBConnMaxLifetime:                5 * time.Minute,
		FuzzyMatchEnabled:                true,
		FuzzyMaxEditDistance:             2,
		PageRankRecomputeIntervalMinutes: 60,
		ANNSearchEnabled:                 true,
		MaxRetainedCrawlJobs:             200,
		MaxConcurrentCrawls:              3,
		DefaultRenderer:                  domain.RendererNone,
		LinkScope:                        domain.LinkScopeTLD,
		MaxDocumentVersions:              3,
		TitleWeight:                      2,
		EmbeddingHashEnabled:             true,
		EmbeddingSearchWeights:           map[string]float64{domain.EmbeddingProviderHash: 1},
		EmbeddingTitleWeight:             0.3,
		URLAliasWWWEnabled:               true,
		ContentDedupEnabled:              true,
		ContentDedupMethod:               domain.ContentDedupMethodExact,
		ContentDedupSimHashMaxDistance:   3,
		ContentDedupIntervalMinutes:      120,
		EmbeddingRecomputeConcurrency:    4,
	}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("expected defaults %+v, got %+v", want, v)
	}
}

func TestOperationalSettings_SetZeroMaxDocumentVersionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxDocumentVersions: 0})
	if v := s.Get(); v.MaxDocumentVersions != 3 {
		t.Errorf("expected a zero MaxDocumentVersions to fall back to the default 3, got %d", v.MaxDocumentVersions)
	}
}

func TestOperationalSettings_SetNegativeMaxDocumentVersionsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxDocumentVersions: -5})
	if v := s.Get(); v.MaxDocumentVersions != 3 {
		t.Errorf("expected a negative MaxDocumentVersions to fall back to the default 3, got %d", v.MaxDocumentVersions)
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

func TestOperationalSettings_SetZeroMaxConcurrentCrawlsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxConcurrentCrawls: 0})
	if v := s.Get(); v.MaxConcurrentCrawls != 3 {
		t.Errorf("expected a zero MaxConcurrentCrawls to fall back to the default 3, got %d", v.MaxConcurrentCrawls)
	}
}

func TestOperationalSettings_SetNegativeMaxConcurrentCrawlsFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxConcurrentCrawls: -5})
	if v := s.Get(); v.MaxConcurrentCrawls != 3 {
		t.Errorf("expected a negative MaxConcurrentCrawls to fall back to the default 3, got %d", v.MaxConcurrentCrawls)
	}
}

func TestOperationalSettings_SetPositiveMaxConcurrentCrawlsPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MaxConcurrentCrawls: 10})
	if v := s.Get(); v.MaxConcurrentCrawls != 10 {
		t.Errorf("expected MaxConcurrentCrawls=10 to be preserved, got %d", v.MaxConcurrentCrawls)
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

func TestOperationalSettings_SetBlankLinkScopeFallsBackToTLD(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: ""})
	if v := s.Get(); v.LinkScope != domain.LinkScopeTLD {
		t.Errorf("expected a blank LinkScope to fall back to LinkScopeTLD, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetInvalidLinkScopeFallsBackToTLD(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: "planet"})
	if v := s.Get(); v.LinkScope != domain.LinkScopeTLD {
		t.Errorf("expected an unrecognized LinkScope to fall back to LinkScopeTLD, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetValidLinkScopePreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{LinkScope: domain.LinkScopeAny})
	if v := s.Get(); v.LinkScope != domain.LinkScopeAny {
		t.Errorf("expected LinkScope=any to be preserved, got %q", v.LinkScope)
	}
}

func TestOperationalSettings_SetBlankContentDedupMethodFallsBackToExact(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupMethod: ""})
	if v := s.Get(); v.ContentDedupMethod != domain.ContentDedupMethodExact {
		t.Errorf("expected a blank ContentDedupMethod to fall back to exact, got %q", v.ContentDedupMethod)
	}
}

func TestOperationalSettings_SetInvalidContentDedupMethodFallsBackToExact(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupMethod: "fuzzy-hash"})
	if v := s.Get(); v.ContentDedupMethod != domain.ContentDedupMethodExact {
		t.Errorf("expected an unrecognized ContentDedupMethod to fall back to exact, got %q", v.ContentDedupMethod)
	}
}

func TestOperationalSettings_SetSimHashContentDedupMethodPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupMethod: domain.ContentDedupMethodSimHash})
	if v := s.Get(); v.ContentDedupMethod != domain.ContentDedupMethodSimHash {
		t.Errorf("expected ContentDedupMethod=simhash to be preserved, got %q", v.ContentDedupMethod)
	}
}

func TestOperationalSettings_SetZeroContentDedupSimHashMaxDistanceFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupSimHashMaxDistance: 0})
	if v := s.Get(); v.ContentDedupSimHashMaxDistance != 3 {
		t.Errorf("expected a zero ContentDedupSimHashMaxDistance to fall back to the default 3, got %d", v.ContentDedupSimHashMaxDistance)
	}
}

func TestOperationalSettings_SetContentDedupSimHashMaxDistanceClampedToRange(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupSimHashMaxDistance: 99})
	if v := s.Get(); v.ContentDedupSimHashMaxDistance != 10 {
		t.Errorf("expected ContentDedupSimHashMaxDistance clamped to 10, got %d", v.ContentDedupSimHashMaxDistance)
	}
}

func TestOperationalSettings_SetZeroContentDedupIntervalMinutesFallsBackToDefault(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupIntervalMinutes: 0})
	if v := s.Get(); v.ContentDedupIntervalMinutes != 120 {
		t.Errorf("expected a zero ContentDedupIntervalMinutes to fall back to the default 120, got %d", v.ContentDedupIntervalMinutes)
	}
}

func TestOperationalSettings_SetContentDedupIntervalMinutesBelowFloorClampedUp(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{ContentDedupIntervalMinutes: 1})
	if v := s.Get(); v.ContentDedupIntervalMinutes != 15 {
		t.Errorf("expected ContentDedupIntervalMinutes clamped up to the 15-minute floor, got %d", v.ContentDedupIntervalMinutes)
	}
}

// TestOperationalSettings_SetContentDedupEnabledPassedThroughUnvalidated
// proves ContentDedupEnabled (unlike every clamped field above) is simply
// passed through -- false is both the zero value and a legitimate,
// deliberate "don't merge anything" choice, not something to self-heal.
func TestOperationalSettings_SetContentDedupEnabledPassedThroughUnvalidated(t *testing.T) {
	s := domain.NewOperationalSettings(domain.OperationalSettingsValues{ContentDedupEnabled: true})
	if v := s.Get(); !v.ContentDedupEnabled {
		t.Error("expected ContentDedupEnabled=true to be preserved")
	}
	s.Set(domain.OperationalSettingsValues{ContentDedupEnabled: false})
	if v := s.Get(); v.ContentDedupEnabled {
		t.Error("expected ContentDedupEnabled=false to be preserved, not defaulted back to true")
	}
}

// TestOperationalSettings_SetEmbeddingSearchWeightsPassedThroughUnvalidated
// proves Set doesn't self-heal EmbeddingSearchWeights itself (an entry
// naming an unrecognized or not-currently-enabled provider, or an empty
// map) -- that validation is domain.ReconcileSearchWeights' job (see its
// own tests), applied by callers that have both the settings and the live
// embedding_http_endpoints list at hand, since provider validity depends
// on that dynamically configured table rather than a fixed enum Set could
// check on its own.
func TestOperationalSettings_SetEmbeddingSearchWeightsPassedThroughUnvalidated(t *testing.T) {
	cases := []map[string]float64{
		nil,
		{},
		{"ouija-board": 1},
		{"some-deleted-endpoint-id": 0.5, domain.EmbeddingProviderHash: 0.5},
	}
	for _, weights := range cases {
		s := domain.DefaultOperationalSettings()
		s.Set(domain.OperationalSettingsValues{EmbeddingSearchWeights: weights})
		got := s.Get().EmbeddingSearchWeights
		if len(got) != len(weights) {
			t.Errorf("Set(EmbeddingSearchWeights=%v): expected it passed through unchanged, got %v", weights, got)
			continue
		}
		for k, v := range weights {
			if got[k] != v {
				t.Errorf("Set(EmbeddingSearchWeights=%v): expected it passed through unchanged, got %v", weights, got)
			}
		}
	}
}

// TestOperationalSettings_SetEmbeddingProviderPassedThroughUnvalidated proves
// the deprecated EmbeddingProvider field (kept only so a settings blob
// saved before EmbeddingSearchWeights existed still decodes -- see its own
// doc comment) is likewise never touched by Set.
func TestOperationalSettings_SetEmbeddingProviderPassedThroughUnvalidated(t *testing.T) {
	cases := []string{"", "ouija-board", "some-deleted-endpoint-id"}
	for _, name := range cases {
		s := domain.DefaultOperationalSettings()
		s.Set(domain.OperationalSettingsValues{EmbeddingProvider: name})
		if v := s.Get(); v.EmbeddingProvider != name {
			t.Errorf("Set(EmbeddingProvider=%q): expected it passed through unchanged, got %q", name, v.EmbeddingProvider)
		}
	}
}

func TestOperationalSettings_SetEmbeddingHashEnabledPreserved(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{EmbeddingHashEnabled: false})
	if v := s.Get(); v.EmbeddingHashEnabled {
		t.Errorf("expected EmbeddingHashEnabled=false to be preserved (Set no longer forces it back to true), got %+v", v)
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
