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

func TestDefaultOperationalSettings_ReturnsBuiltInDefaults(t *testing.T) {
	v := domain.DefaultOperationalSettings().Get()
	want := domain.OperationalSettingsValues{
		FetchTimeout:     8 * time.Second,
		UserAgent:        "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
		DefaultMaxPages:  20,
		MinTextLength:    50,
		DefaultTopK:      10,
		SessionTTL:       12 * time.Hour,
		CrawlDelayMs:     250,
		MaxResponseBytes: 5 * 1024 * 1024,
	}
	if v != want {
		t.Errorf("expected defaults %+v, got %+v", want, v)
	}
}
