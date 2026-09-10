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
		FetchTimeout:    3 * time.Second,
		UserAgent:       "custom/1.0",
		DefaultMaxPages: 5,
		MinTextLength:   10,
		DefaultTopK:     3,
		SessionTTL:      2 * time.Hour,
	})
	v := s.Get()
	if v.FetchTimeout != 3*time.Second || v.UserAgent != "custom/1.0" || v.DefaultMaxPages != 5 ||
		v.MinTextLength != 10 || v.DefaultTopK != 3 || v.SessionTTL != 2*time.Hour {
		t.Errorf("unexpected values after Set, got %+v", v)
	}
}

func TestOperationalSettings_SetSubstitutesDefaultsForZeroValues(t *testing.T) {
	s := domain.NewOperationalSettings(domain.OperationalSettingsValues{})
	v := s.Get()
	d := domain.DefaultOperationalSettings().Get()
	if v.FetchTimeout != d.FetchTimeout || v.UserAgent != d.UserAgent || v.DefaultMaxPages != d.DefaultMaxPages ||
		v.DefaultTopK != d.DefaultTopK || v.SessionTTL != d.SessionTTL {
		t.Errorf("expected zero-valued fields to fall back to defaults, got %+v", v)
	}
	if v.MinTextLength != 0 {
		t.Errorf("expected MinTextLength=0 to be preserved (not defaulted), got %d", v.MinTextLength)
	}
}

func TestOperationalSettings_SetNegativeMinTextLengthClampsToZero(t *testing.T) {
	s := domain.DefaultOperationalSettings()
	s.Set(domain.OperationalSettingsValues{MinTextLength: -5})
	if v := s.Get(); v.MinTextLength != 0 {
		t.Errorf("expected negative MinTextLength clamped to 0, got %d", v.MinTextLength)
	}
}

func TestDefaultOperationalSettings_ReturnsBuiltInDefaults(t *testing.T) {
	v := domain.DefaultOperationalSettings().Get()
	want := domain.OperationalSettingsValues{
		FetchTimeout:    8 * time.Second,
		UserAgent:       "OwnSearchEngine/1.0 (+educational)",
		DefaultMaxPages: 20,
		MinTextLength:   50,
		DefaultTopK:     10,
		SessionTTL:      12 * time.Hour,
	}
	if v != want {
		t.Errorf("expected defaults %+v, got %+v", want, v)
	}
}
