package domain

import "testing"

func TestChatEndpointClamp(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero uses default", 0, DefaultChatRAGResultCount},
		{"negative uses default", -5, DefaultChatRAGResultCount},
		{"within range unchanged", 3, 3},
		{"at min unchanged", MinChatRAGResultCount, MinChatRAGResultCount},
		{"at max unchanged", MaxChatRAGResultCount, MaxChatRAGResultCount},
		{"above max clamps to max", MaxChatRAGResultCount + 100, MaxChatRAGResultCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &ChatEndpoint{RAGResultCount: tc.in}
			e.Clamp()
			if e.RAGResultCount != tc.want {
				t.Errorf("Clamp() with RAGResultCount=%d: got %d, want %d", tc.in, e.RAGResultCount, tc.want)
			}
		})
	}
}

func TestChatEndpointClamp_WebSearchResultCount(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero uses default", 0, DefaultChatWebSearchResultCount},
		{"negative uses default", -5, DefaultChatWebSearchResultCount},
		{"within range unchanged", 3, 3},
		{"at min unchanged", MinChatWebSearchResultCount, MinChatWebSearchResultCount},
		{"at max unchanged", MaxChatWebSearchResultCount, MaxChatWebSearchResultCount},
		{"above max clamps to max", MaxChatWebSearchResultCount + 100, MaxChatWebSearchResultCount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &ChatEndpoint{WebSearchResultCount: tc.in}
			e.Clamp()
			if e.WebSearchResultCount != tc.want {
				t.Errorf("Clamp() with WebSearchResultCount=%d: got %d, want %d", tc.in, e.WebSearchResultCount, tc.want)
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
