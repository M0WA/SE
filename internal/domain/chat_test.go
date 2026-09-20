package domain

import "testing"

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
