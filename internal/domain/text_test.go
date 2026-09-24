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

func TestTruncateWithEllipsis_ShortStringReturnedAsIs(t *testing.T) {
	if got := domain.TruncateWithEllipsis("short", 10); got != "short" {
		t.Errorf("expected unchanged string, got %q", got)
	}
}

func TestTruncateWithEllipsis_LongStringCappedWithEllipsis(t *testing.T) {
	s := strings.Repeat("x", 20)
	got := domain.TruncateWithEllipsis(s, 10)
	want := strings.Repeat("x", 10) + "..."
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestRedactSecret_ReplacesEveryOccurrence(t *testing.T) {
	got := domain.RedactSecret("key=sk-abc123 and again sk-abc123", "sk-abc123")
	want := "key=[REDACTED] and again [REDACTED]"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestRedactSecret_EmptySecretIsANoop(t *testing.T) {
	if got := domain.RedactSecret("nothing to redact here", ""); got != "nothing to redact here" {
		t.Errorf("expected unchanged string, got %q", got)
	}
}
