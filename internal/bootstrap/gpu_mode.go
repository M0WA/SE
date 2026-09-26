package bootstrap

import (
	"context"
	"log"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// DecryptingGPUModeStore wraps a ports.GPUModeStore, decrypting
// ControlAPIKey on every GetGPUModeSettings call -- same reasoning as
// DecryptingChatVisionStore (chat_vision.go): gpu mode settings are read
// live, not just once at startup (application.GPUModeService reloads
// them on every Status/Switch/Heartbeat call, since an admin can change
// them without a restart), so decryption happens here, at the one point
// cmd/search's own construction can reach both the raw store and
// SETTINGS_ENCRYPTION_KEY, keeping internal/application free of any
// adapter dependency.
type DecryptingGPUModeStore struct {
	ports.GPUModeStore
	settingsEncryptionKey []byte
}

// NewDecryptingGPUModeStore wraps store, decrypting ControlAPIKey with
// settingsEncryptionKey on every read. A nil settingsEncryptionKey is
// fine (settingscrypto.Decrypt treats an unconfigured key as
// already-plaintext).
func NewDecryptingGPUModeStore(store ports.GPUModeStore, settingsEncryptionKey []byte) *DecryptingGPUModeStore {
	return &DecryptingGPUModeStore{GPUModeStore: store, settingsEncryptionKey: settingsEncryptionKey}
}

// GetGPUModeSettings overrides the embedded store's method, decrypting
// ControlAPIKey before returning. A decryption failure is logged and
// clears the key (same fail-fast-and-visible convention as
// DecryptingChatVisionStore), never a reason to fail the whole call --
// SetGPUModeSettings is unaffected, promoted from ports.GPUModeStore
// unchanged.
func (d *DecryptingGPUModeStore) GetGPUModeSettings(ctx context.Context) (domain.GPUModeSettings, error) {
	v, err := d.GPUModeStore.GetGPUModeSettings(ctx)
	if err != nil {
		return v, err
	}
	dec, decErr := settingscrypto.Decrypt(d.settingsEncryptionKey, v.ControlAPIKey)
	if decErr != nil {
		log.Printf("decrypting gpu mode control API key: %v", decErr)
		v.ControlAPIKey = ""
		return v, nil
	}
	v.ControlAPIKey = dec
	return v, nil
}
