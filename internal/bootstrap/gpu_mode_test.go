package bootstrap_test

import (
	"context"
	"errors"
	"testing"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
)

// fakeGPUModeStore is a minimal ports.GPUModeStore fake for testing
// DecryptingGPUModeStore's decoration.
type fakeGPUModeStore struct {
	settings domain.GPUModeSettings
	getErr   error
	setCalls []domain.GPUModeSettings
}

func (f *fakeGPUModeStore) GetGPUModeSettings(context.Context) (domain.GPUModeSettings, error) {
	if f.getErr != nil {
		return domain.GPUModeSettings{}, f.getErr
	}
	return f.settings, nil
}

func (f *fakeGPUModeStore) SetGPUModeSettings(_ context.Context, v domain.GPUModeSettings) error {
	f.setCalls = append(f.setCalls, v)
	return nil
}

func TestDecryptingGPUModeStore_NilKeyPassesPlaintextThrough(t *testing.T) {
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{ControlAPIKey: "sk-plain"}}
	d := bootstrap.NewDecryptingGPUModeStore(store, nil)
	v, err := d.GetGPUModeSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.ControlAPIKey != "sk-plain" {
		t.Errorf("expected the plaintext key unchanged, got %q", v.ControlAPIKey)
	}
}

func TestDecryptingGPUModeStore_DecryptsAnEncryptedValue(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{ControlAPIKey: enc}}
	d := bootstrap.NewDecryptingGPUModeStore(store, key)
	v, err := d.GetGPUModeSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.ControlAPIKey != "sk-real-secret" {
		t.Errorf("expected the decrypted key, got %q", v.ControlAPIKey)
	}
}

func TestDecryptingGPUModeStore_FailureClearsTheKeyRatherThanLeakingCiphertext(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := &fakeGPUModeStore{settings: domain.GPUModeSettings{ControlAPIKey: enc}}
	d := bootstrap.NewDecryptingGPUModeStore(store, nil)
	v, err := d.GetGPUModeSettings(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.ControlAPIKey != "" {
		t.Errorf("expected the key cleared on a decryption failure, got %q", v.ControlAPIKey)
	}
}

// TestDecryptingGPUModeStore_UnderlyingStoreErrorPropagates proves a
// GetGPUModeSettings error from the wrapped store passes straight
// through, never masked as a decryption failure.
func TestDecryptingGPUModeStore_UnderlyingStoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db unavailable")
	store := &fakeGPUModeStore{getErr: wantErr}
	d := bootstrap.NewDecryptingGPUModeStore(store, nil)
	_, err := d.GetGPUModeSettings(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the underlying store's error to propagate, got %v", err)
	}
}

// TestDecryptingGPUModeStore_SetGPUModeSettingsIsPromotedUnchanged proves
// the embedded ports.GPUModeStore's SetGPUModeSettings is used as-is (no
// encryption happens here -- that's restapi's job before ever calling
// Set), just promoted through the embedding.
func TestDecryptingGPUModeStore_SetGPUModeSettingsIsPromotedUnchanged(t *testing.T) {
	store := &fakeGPUModeStore{}
	d := bootstrap.NewDecryptingGPUModeStore(store, nil)
	want := domain.GPUModeSettings{ControlAPIKey: "enc:v1:whatever"}
	if err := d.SetGPUModeSettings(context.Background(), want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.setCalls) != 1 || store.setCalls[0].ControlAPIKey != want.ControlAPIKey {
		t.Errorf("expected Set called through unchanged, got %+v", store.setCalls)
	}
}
