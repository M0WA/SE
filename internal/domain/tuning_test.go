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
