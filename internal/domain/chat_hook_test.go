package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNewChatHookID_SlugifiesName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"web_search", "web_search"},
		{"Web Search", "web_search"},
		{"  leading and trailing  ", "leading_and_trailing"},
		{"Multiple   Spaces", "multiple_spaces"},
	}
	for _, tc := range cases {
		if got := domain.NewChatHookID(tc.name, nil); got != tc.want {
			t.Errorf("NewChatHookID(%q, nil) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNewChatHookID_TruncatesToPatternMaxLength(t *testing.T) {
	got := domain.NewChatHookID("a very long descriptive hook name indeed", nil)
	if len(got) > 20 {
		t.Errorf("expected ID truncated to at most 20 chars, got %q (%d chars)", got, len(got))
	}
	if !domain.ChatHookIDPattern.MatchString(got) {
		t.Errorf("expected %q to match ChatHookIDPattern", got)
	}
}

func TestNewChatHookID_NoAlphanumericFallsBackToTimestamp(t *testing.T) {
	got := domain.NewChatHookID("!!!", nil)
	if got == "" {
		t.Error("expected a non-empty fallback ID for a name with no alphanumeric characters")
	}
	if !domain.ChatHookIDPattern.MatchString(got) {
		t.Errorf("expected fallback ID %q to match ChatHookIDPattern", got)
	}
}

func TestNewChatHookID_DedupesAgainstExisting(t *testing.T) {
	existing := map[string]bool{"web_search": true}
	got := domain.NewChatHookID("web_search", existing)
	if got != "web_search_2" {
		t.Errorf("expected the first collision to dedupe to \"web_search_2\", got %q", got)
	}
}

func TestNewChatHookID_DedupesPastMultipleCollisions(t *testing.T) {
	existing := map[string]bool{"hook": true, "hook_2": true, "hook_3": true}
	got := domain.NewChatHookID("hook", existing)
	if got != "hook_4" {
		t.Errorf("expected dedupe to skip past every taken suffix, got %q", got)
	}
}

func TestNewChatHookID_DedupeRespectsLengthCapWithSuffix(t *testing.T) {
	longName := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // slugifies/truncates to 20 a's
	base := domain.NewChatHookID(longName, nil)
	existing := map[string]bool{base: true}
	got := domain.NewChatHookID(longName, existing)
	if len(got) > 20 {
		t.Errorf("expected deduped ID truncated to at most 20 chars, got %q (%d chars)", got, len(got))
	}
	if !domain.ChatHookIDPattern.MatchString(got) {
		t.Errorf("expected deduped ID %q to match ChatHookIDPattern", got)
	}
	if got == base {
		t.Errorf("expected a colliding name to produce a different ID, got %q twice", got)
	}
}
