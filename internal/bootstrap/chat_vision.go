package bootstrap

import (
	"context"
	"log"

	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// DecryptingChatVisionStore wraps a ports.ChatVisionStore, decrypting
// CaptionAPIKey on every GetChatVisionSettings call -- unlike embedding
// endpoints (decrypted once at startup, see LoadEmbeddingEndpoints), chat
// vision settings are read live once per chat turn (an admin can change
// them without a restart), so decryption happens here, at the one point
// cmd/search's own construction can reach both the raw store and
// SETTINGS_ENCRYPTION_KEY, keeping internal/application free of any
// adapter dependency (see chat_service.go).
type DecryptingChatVisionStore struct {
	ports.ChatVisionStore
	settingsEncryptionKey []byte
}

// NewDecryptingChatVisionStore wraps store, decrypting CaptionAPIKey with
// settingsEncryptionKey on every read. A nil settingsEncryptionKey is fine
// (settingscrypto.Decrypt treats an unconfigured key as already-plaintext).
func NewDecryptingChatVisionStore(store ports.ChatVisionStore, settingsEncryptionKey []byte) *DecryptingChatVisionStore {
	return &DecryptingChatVisionStore{ChatVisionStore: store, settingsEncryptionKey: settingsEncryptionKey}
}

// GetChatVisionSettings overrides the embedded store's method, decrypting
// CaptionAPIKey before returning. A decryption failure is logged and
// clears the key (same fail-fast-and-visible convention as
// DecryptEndpointAPIKey), never a reason to fail the whole call --
// SetChatVisionSettings is unaffected, promoted from ports.ChatVisionStore
// unchanged.
func (d *DecryptingChatVisionStore) GetChatVisionSettings(ctx context.Context) (domain.ChatVisionSettings, error) {
	v, err := d.ChatVisionStore.GetChatVisionSettings(ctx)
	if err != nil {
		return v, err
	}
	dec, decErr := settingscrypto.Decrypt(d.settingsEncryptionKey, v.CaptionAPIKey)
	if decErr != nil {
		log.Printf("decrypting chat vision caption API key: %v", decErr)
		v.CaptionAPIKey = ""
		return v, nil
	}
	v.CaptionAPIKey = dec
	return v, nil
}
