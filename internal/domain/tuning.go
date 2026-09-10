package domain

import "sync"

// TuningSettings holds the hybrid search's runtime-adjustable scoring
// parameters. Safe for concurrent use: read on every search request,
// written from the admin tuning panel.
type TuningSettings struct {
	mu    sync.RWMutex
	alpha float64
	k1    float64
	b     float64
}

func NewTuningSettings(alpha, k1, b float64) *TuningSettings {
	return &TuningSettings{alpha: alpha, k1: k1, b: b}
}

// Get returns the current alpha (BM25 vs. semantic blend weight, 0-1), k1
// (term-frequency saturation) and b (length normalization) values.
func (s *TuningSettings) Get() (alpha, k1, b float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alpha, s.k1, s.b
}

// Set updates the tuning parameters. Values are clamped to sane ranges
// rather than rejected, since this is an admin convenience knob, not a
// user-facing form that needs field-level validation errors.
func (s *TuningSettings) Set(alpha, k1, b float64) {
	alpha = clamp(alpha, 0, 1)
	if k1 < 0 {
		k1 = 0
	}
	b = clamp(b, 0, 1)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.alpha, s.k1, s.b = alpha, k1, b
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
