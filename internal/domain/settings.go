package domain

import (
	"sync"
	"time"
)

// OperationalSettingsValues is a snapshot of every admin-configurable
// operational knob outside the BM25/semantic ranking formula (see
// TuningSettings for that): how the crawler fetches pages, what a search
// returns by default, and how long a sign-in session lasts.
type OperationalSettingsValues struct {
	FetchTimeout    time.Duration
	UserAgent       string
	DefaultMaxPages int
	MinTextLength   int
	DefaultTopK     int
	SessionTTL      time.Duration
}

func defaultOperationalSettings() OperationalSettingsValues {
	return OperationalSettingsValues{
		FetchTimeout:    8 * time.Second,
		UserAgent:       "OwnSearchEngine/1.0 (+educational)",
		DefaultMaxPages: 20,
		MinTextLength:   50,
		DefaultTopK:     10,
		SessionTTL:      12 * time.Hour,
	}
}

// OperationalSettings holds those values, safe for concurrent use: read on
// every fetch/crawl/search/login, written from the admin tuning panel.
type OperationalSettings struct {
	mu sync.RWMutex
	v  OperationalSettingsValues
}

func NewOperationalSettings(v OperationalSettingsValues) *OperationalSettings {
	s := &OperationalSettings{}
	s.Set(v)
	return s
}

// DefaultOperationalSettings constructs settings using the built-in
// defaults, for callers that don't need to override anything at startup.
func DefaultOperationalSettings() *OperationalSettings {
	return NewOperationalSettings(defaultOperationalSettings())
}

// Get returns the current values. A nil *OperationalSettings (e.g. a test
// that doesn't care about these knobs) returns the built-in defaults
// rather than a zero-value struct, so callers never need a separate nil
// check before reading.
func (s *OperationalSettings) Get() OperationalSettingsValues {
	if s == nil {
		return defaultOperationalSettings()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.v
}

// Set updates the settings, substituting the default for any field left
// at its zero value rather than rejecting the update -- this is an admin
// convenience knob, not a user-facing form that needs field-level
// validation errors.
func (s *OperationalSettings) Set(v OperationalSettingsValues) {
	if s == nil {
		return
	}
	d := defaultOperationalSettings()
	if v.FetchTimeout <= 0 {
		v.FetchTimeout = d.FetchTimeout
	}
	if v.UserAgent == "" {
		v.UserAgent = d.UserAgent
	}
	if v.DefaultMaxPages <= 0 {
		v.DefaultMaxPages = d.DefaultMaxPages
	}
	if v.MinTextLength < 0 {
		v.MinTextLength = 0
	}
	if v.DefaultTopK <= 0 {
		v.DefaultTopK = d.DefaultTopK
	}
	if v.SessionTTL <= 0 {
		v.SessionTTL = d.SessionTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.v = v
}
