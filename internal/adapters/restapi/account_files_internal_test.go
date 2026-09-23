package restapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"searchengine/internal/domain"
)

// stubChatStore is a minimal ports.ChatStore fake for this white-box test
// file (account_files_test.go's fakeChatStore lives in the external package).
type stubChatStore struct {
	chats   []domain.PersistedChat
	listErr error
}

func (s *stubChatStore) ListChats(ctx context.Context, ownerUserID string) ([]domain.PersistedChat, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.chats, nil
}
func (s *stubChatStore) CreateChat(ctx context.Context, c domain.PersistedChat) (domain.PersistedChat, error) {
	return domain.PersistedChat{}, nil
}
func (s *stubChatStore) UpdateChat(ctx context.Context, c domain.PersistedChat) error { return nil }
func (s *stubChatStore) DeleteChat(ctx context.Context, ownerUserID, id string) error { return nil }

func TestUserOwnsChat_NilChatsReturnsFalse(t *testing.T) {
	h := &Handler{}
	if h.userOwnsChat(context.Background(), "alice", "c1") {
		t.Error("expected false when chats is not configured")
	}
}

func TestUserOwnsChat_StoreErrorReturnsFalse(t *testing.T) {
	h := &Handler{chats: &stubChatStore{listErr: errors.New("boom")}}
	if h.userOwnsChat(context.Background(), "alice", "c1") {
		t.Error("expected false when ListChats errors")
	}
}

func TestUserOwnsChat_OwnedChatReturnsTrue(t *testing.T) {
	h := &Handler{chats: &stubChatStore{chats: []domain.PersistedChat{{ID: "c1", OwnerUserID: "alice"}}}}
	if !h.userOwnsChat(context.Background(), "alice", "c1") {
		t.Error("expected true for an owned chat")
	}
}

func TestUserOwnsChat_UnownedChatReturnsFalse(t *testing.T) {
	h := &Handler{chats: &stubChatStore{chats: []domain.PersistedChat{{ID: "c1", OwnerUserID: "alice"}}}}
	if h.userOwnsChat(context.Background(), "alice", "someone-elses-chat") {
		t.Error("expected false for a chat id that isn't in the store")
	}
}

func TestFileAccessTokenFor_ForeignChatIDIsDroppedNotTrusted(t *testing.T) {
	h := &Handler{
		fileTokens: newFileTokenStore(),
		chats:      &stubChatStore{chats: []domain.PersistedChat{{ID: "c1", OwnerUserID: "alice"}}},
	}
	token := h.fileAccessTokenFor(context.Background(), "alice", "not-alices-chat")
	_, chatID, ok := h.fileTokens.resolve(token)
	if !ok {
		t.Fatal("expected a token to still be issued")
	}
	if chatID != "" {
		t.Errorf("expected the unowned chatID to be dropped (unscoped token), got %q", chatID)
	}
}

func TestFileAccessTokenFor_OwnedChatIDIsBakedIn(t *testing.T) {
	h := &Handler{
		fileTokens: newFileTokenStore(),
		chats:      &stubChatStore{chats: []domain.PersistedChat{{ID: "c1", OwnerUserID: "alice"}}},
	}
	token := h.fileAccessTokenFor(context.Background(), "alice", "c1")
	_, chatID, ok := h.fileTokens.resolve(token)
	if !ok {
		t.Fatal("expected a token to be issued")
	}
	if chatID != "c1" {
		t.Errorf("expected chatID %q, got %q", "c1", chatID)
	}
}

func TestFileTokenStore_IssueThenResolveSucceeds(t *testing.T) {
	s := newFileTokenStore()
	token := s.issue("alice", "chat-1")
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	userID, chatID, ok := s.resolve(token)
	if !ok {
		t.Fatal("expected a freshly issued token to resolve")
	}
	if userID != "alice" {
		t.Errorf("expected userID %q, got %q", "alice", userID)
	}
	if chatID != "chat-1" {
		t.Errorf("expected chatID %q, got %q", "chat-1", chatID)
	}
}

func TestFileTokenStore_IssueWithNoChatResolvesEmptyChatID(t *testing.T) {
	s := newFileTokenStore()
	token := s.issue("alice", "")
	_, chatID, ok := s.resolve(token)
	if !ok {
		t.Fatal("expected a freshly issued token to resolve")
	}
	if chatID != "" {
		t.Errorf("expected an empty chatID, got %q", chatID)
	}
}

func TestFileTokenStore_ResolveUnknownTokenReportsFalse(t *testing.T) {
	s := newFileTokenStore()
	_, _, ok := s.resolve("never-issued")
	if ok {
		t.Error("expected an unknown token to not resolve")
	}
}

// TestFileTokenStore_ExpiredTokenReportsFalseAndIsForgotten mirrors
// sessionStore's TestSessionStore_ExpiredSessionReportsInvalidAndIsForgotten.
func TestFileTokenStore_ExpiredTokenReportsFalseAndIsForgotten(t *testing.T) {
	s := newFileTokenStore()
	token := "tok-expired"
	s.tokens[token] = fileTokenRecord{userID: "alice", chatID: "chat-1", expiresAt: time.Now().Add(-time.Second)}

	_, _, ok := s.resolve(token)
	if ok {
		t.Error("expected an expired token to not resolve")
	}
	s.mu.Lock()
	_, stillPresent := s.tokens[token]
	s.mu.Unlock()
	if stillPresent {
		t.Error("expected the expired token to be removed from the store")
	}
}

func TestFileTokenStore_IssueProducesDistinctTokens(t *testing.T) {
	s := newFileTokenStore()
	a := s.issue("alice", "")
	b := s.issue("alice", "")
	if a == b {
		t.Error("expected two calls to produce distinct tokens")
	}
}
