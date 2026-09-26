package main

import "testing"

func TestEnvInt(t *testing.T) {
	t.Setenv("GPU_CONTROL_TEST_INT", "42")
	if got := envInt("GPU_CONTROL_TEST_INT", 7); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestEnvInt_FallsBackWhenUnset(t *testing.T) {
	if got := envInt("GPU_CONTROL_TEST_INT_UNSET", 7); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}

func TestEnvInt_FallsBackWhenUnparseable(t *testing.T) {
	t.Setenv("GPU_CONTROL_TEST_INT_BAD", "not-a-number")
	if got := envInt("GPU_CONTROL_TEST_INT_BAD", 7); got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}
