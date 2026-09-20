package domain_test

import (
	"strings"
	"testing"

	"searchengine/internal/domain"
)

func TestTruncateWithNote_ShortStringReturnedAsIs(t *testing.T) {
	if got := domain.TruncateWithNote("short", 10); got != "short" {
		t.Errorf("expected unchanged string, got %q", got)
	}
}

func TestTruncateWithNote_LongStringCappedWithByteCountNote(t *testing.T) {
	s := strings.Repeat("x", 20)
	got := domain.TruncateWithNote(s, 10)
	want := strings.Repeat("x", 10) + "... [truncated, 20 bytes total]"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
