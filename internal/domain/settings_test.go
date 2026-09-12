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
	}
	if v != want {
		t.Errorf("expected defaults %+v, got %+v", want, v)
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
