package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

// fakeChatVisionStore is a minimal ports.ChatVisionStore fake for testing
// DecryptingChatVisionStore's decoration.
type fakeChatVisionStore struct {
	settings domain.ChatVisionSettings
	getErr   error
	setCalls []domain.ChatVisionSettings
}

func (f *fakeChatVisionStore) GetChatVisionSettings(context.Context) (domain.ChatVisionSettings, error) {
	if f.getErr != nil {
		return domain.ChatVisionSettings{}, f.getErr
	}
	return f.settings, nil
}

func (f *fakeChatVisionStore) SetChatVisionSettings(_ context.Context, v domain.ChatVisionSettings) error {
	f.setCalls = append(f.setCalls, v)
	return nil
}

func TestDecryptingChatVisionStore_NilKeyPassesPlaintextThrough(t *testing.T) {
	store := &fakeChatVisionStore{settings: domain.ChatVisionSettings{CaptionAPIKey: "sk-plain"}}
	d := bootstrap.NewDecryptingChatVisionStore(store, nil)
	v, err := d.GetChatVisionSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.CaptionAPIKey != "sk-plain" {
		t.Errorf("expected the plaintext key unchanged, got %q", v.CaptionAPIKey)
	}
}

func TestDecryptingChatVisionStore_DecryptsAnEncryptedValue(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := &fakeChatVisionStore{settings: domain.ChatVisionSettings{CaptionAPIKey: enc}}
	d := bootstrap.NewDecryptingChatVisionStore(store, key)
	v, err := d.GetChatVisionSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.CaptionAPIKey != "sk-real-secret" {
		t.Errorf("expected the decrypted key, got %q", v.CaptionAPIKey)
	}
}

func TestDecryptingChatVisionStore_FailureClearsTheKeyRatherThanLeakingCiphertext(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := &fakeChatVisionStore{settings: domain.ChatVisionSettings{CaptionAPIKey: enc}}
	d := bootstrap.NewDecryptingChatVisionStore(store, nil)
	v, err := d.GetChatVisionSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.CaptionAPIKey != "" {
		t.Errorf("expected the key cleared on a decryption failure, got %q", v.CaptionAPIKey)
	}
}

// TestDecryptingChatVisionStore_UnderlyingStoreErrorPropagates proves a
// GetChatVisionSettings error from the wrapped store passes straight
// through, never masked as a decryption failure.
func TestDecryptingChatVisionStore_UnderlyingStoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db unavailable")
	store := &fakeChatVisionStore{getErr: wantErr}
	d := bootstrap.NewDecryptingChatVisionStore(store, nil)
	_, err := d.GetChatVisionSettings(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the underlying store's error to propagate, got %v", err)
	}
}

// TestDecryptingChatVisionStore_SetChatVisionSettingsIsPromotedUnchanged
// proves the embedded ports.ChatVisionStore's SetChatVisionSettings is
// used as-is (no encryption happens here -- that's restapi's job before
// ever calling Set), just promoted through the embedding.
func TestDecryptingChatVisionStore_SetChatVisionSettingsIsPromotedUnchanged(t *testing.T) {
	store := &fakeChatVisionStore{}
	d := bootstrap.NewDecryptingChatVisionStore(store, nil)
	want := domain.ChatVisionSettings{CaptionAPIKey: "enc:v1:whatever"}
	if err := d.SetChatVisionSettings(context.Background(), want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.setCalls) != 1 || store.setCalls[0].CaptionAPIKey != want.CaptionAPIKey {
		t.Errorf("expected Set called through unchanged, got %+v", store.setCalls)
	}
}
