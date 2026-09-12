package domain_test

import (
	"sync"
	"testing"

	"searchengine/internal/domain"
)

func TestTuningSettings_GetReturnsConstructedValues(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	alpha, k1, b := s.Get()
	if alpha != 0.5 || k1 != 1.2 || b != 0.75 {
		t.Errorf("expected (0.5, 1.2, 0.75), got (%v, %v, %v)", alpha, k1, b)
	}
}

func TestTuningSettings_SetUpdatesValues(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	s.Set(0.8, 2.0, 0.5)
	alpha, k1, b := s.Get()
	if alpha != 0.8 || k1 != 2.0 || b != 0.5 {
		t.Errorf("expected (0.8, 2.0, 0.5), got (%v, %v, %v)", alpha, k1, b)
	}
}

func TestTuningSettings_SetClampsOutOfRangeValues(t *testing.T) {
	s := domain.NewTuningSettings(0, 0, 0)
	s.Set(1.5, -1, -0.2)
	alpha, k1, b := s.Get()
	if alpha != 1 {
		t.Errorf("expected alpha clamped to 1, got %v", alpha)
	}
	if k1 != 0 {
		t.Errorf("expected negative k1 clamped to 0, got %v", k1)
	}
	if b != 0 {
		t.Errorf("expected negative b clamped to 0, got %v", b)
	}
}

func TestTuningSettings_ValuesReturnsSnapshot(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	v := s.Values()
	if v.Alpha != 0.5 || v.K1 != 1.2 || v.B != 0.75 {
		t.Errorf("expected {0.5, 1.2, 0.75}, got %+v", v)
	}
}

func TestTuningSettings_SetValuesUpdatesAndClamps(t *testing.T) {
	s := domain.NewTuningSettings(0, 0, 0)
	s.SetValues(domain.TuningValues{Alpha: 1.5, K1: -1, B: 0.5})
	alpha, k1, b := s.Get()
	if alpha != 1 || k1 != 0 || b != 0.5 {
		t.Errorf("expected SetValues to clamp like Set, got (%v, %v, %v)", alpha, k1, b)
	}
}

func TestTuningSettings_PageRankWeightDefaultsToZero(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	if w := s.PageRankWeight(); w != 0 {
		t.Errorf("expected PageRankWeight to default to 0, got %v", w)
	}
}

func TestTuningSettings_SetPageRankWeightClampsToUnitRange(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	s.SetPageRankWeight(1.5)
	if w := s.PageRankWeight(); w != 1 {
		t.Errorf("expected PageRankWeight clamped to 1, got %v", w)
	}
	s.SetPageRankWeight(-0.5)
	if w := s.PageRankWeight(); w != 0 {
		t.Errorf("expected negative PageRankWeight clamped to 0, got %v", w)
	}
	s.SetPageRankWeight(0.3)
	if w := s.PageRankWeight(); w != 0.3 {
		t.Errorf("expected PageRankWeight=0.3 to be preserved, got %v", w)
	}
}

func TestTuningSettings_SetDoesNotAffectPageRankWeight(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	s.SetPageRankWeight(0.4)
	s.Set(0.9, 2.0, 0.1)
	if w := s.PageRankWeight(); w != 0.4 {
		t.Errorf("expected Set(alpha,k1,b) to leave PageRankWeight untouched, got %v", w)
	}
}

func TestTuningSettings_ValuesAndSetValuesRoundTripPageRankWeight(t *testing.T) {
	s := domain.NewTuningSettings(0, 0, 0)
	s.SetValues(domain.TuningValues{Alpha: 0.5, K1: 1.2, B: 0.75, PageRankWeight: 0.25})
	v := s.Values()
	if v.PageRankWeight != 0.25 {
		t.Errorf("expected PageRankWeight=0.25 to round-trip through SetValues/Values, got %v", v.PageRankWeight)
	}
	// Out-of-range values are clamped the same way SetPageRankWeight clamps.
	s.SetValues(domain.TuningValues{Alpha: 0.5, K1: 1.2, B: 0.75, PageRankWeight: 5})
	if v := s.Values(); v.PageRankWeight != 1 {
		t.Errorf("expected an out-of-range PageRankWeight to clamp to 1 via SetValues, got %v", v.PageRankWeight)
	}
}

func TestTuningSettings_ConcurrentAccess(t *testing.T) {
	s := domain.NewTuningSettings(0.5, 1.2, 0.75)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s.Set(0.6, 1.3, 0.8) }()
		go func() { defer wg.Done(); s.Get() }()
	}
	wg.Wait()
}
