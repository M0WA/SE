package domain

import "testing"

func TestAutoMaxContextTokens_ReservesQuarterForCompletion(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"typical vLLM max_model_len", 32768, 24576},
		{"zero means nothing detected", 0, 0},
		{"negative treated the same as zero", -5, 0},
		{"small value still reserves proportionally", 1000, 750},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AutoMaxContextTokens(tc.in); got != tc.want {
				t.Errorf("AutoMaxContextTokens(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestChatRoleConstants(t *testing.T) {
	if ChatRoleUser != "user" {
		t.Errorf("ChatRoleUser = %q, want %q", ChatRoleUser, "user")
	}
	if ChatRoleAssistant != "assistant" {
		t.Errorf("ChatRoleAssistant = %q, want %q", ChatRoleAssistant, "assistant")
	}
	if ChatRoleSystem != "system" {
		t.Errorf("ChatRoleSystem = %q, want %q", ChatRoleSystem, "system")
	}
}
