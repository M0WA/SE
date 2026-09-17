// Package settingscrypto encrypts the admin-configured embedding HTTP API
// key before it's persisted to app_settings, decrypting it back only at
// bootstrap.NewEmbedder. Unlike other secrets here it must stay usable as a
// real Authorization header value, so it can't be hashed like a password --
// encrypting at rest with a key that lives only in the env file (never the
// DB) means DB-only access no longer yields the plaintext key.
package settingscrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// encPrefix marks a value Encrypt produced. Anything else -- including "",
// and any value saved before this package existed, or by a deployment that
// has never configured SETTINGS_ENCRYPTION_KEY -- is treated as
// already-plaintext by Decrypt, so this stays backward compatible rather
// than breaking on old data or an unconfigured key.
const encPrefix = "enc:v1:"

// keyLen is 32 bytes: AES-256.
const keyLen = 32

// ParseKey decodes a hex-encoded 32-byte key, e.g. SETTINGS_ENCRYPTION_KEY
// (generate one with `openssl rand -hex 32`). An empty hexKey returns a nil
// key and no error -- encryption is opt-in, like CRAWL_INTERNAL_TOKEN
// elsewhere. A non-empty hexKey that doesn't decode to 32 bytes is a real
// error (almost certainly a typo), worth failing loudly on at startup.
func ParseKey(hexKey string) ([]byte, error) {
	if hexKey == "" {
		return nil, nil
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("settingscrypto: decoding key: %w", err)
	}
	if len(key) != keyLen {
		return nil, fmt.Errorf("settingscrypto: key must be %d bytes (%d hex characters), got %d bytes", keyLen, keyLen*2, len(key))
	}
	return key, nil
}

// Encrypt seals plaintext with key (AES-256-GCM, a random nonce prepended
// to the ciphertext, the whole thing base64-encoded and prefixed with
// encPrefix). A nil key (encryption not configured) or an empty plaintext
// (nothing to protect) returns plaintext unchanged -- the non-breaking,
// opt-in default this codebase's other defense-in-depth additions follow.
func Encrypt(key []byte, plaintext string) (string, error) {
	if key == nil || plaintext == "" {
		return plaintext, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("settingscrypto: generating nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. A value without encPrefix is returned unchanged
// (predates this feature, or no key was ever configured -- all expected).
// A value WITH the prefix but no usable key IS a real error: silently
// returning ciphertext as the real API key would fail confusingly far from
// this actual cause, so it's surfaced here instead.
func Decrypt(key []byte, s string) (string, error) {
	if !strings.HasPrefix(s, encPrefix) {
		return s, nil
	}
	if key == nil {
		return "", errors.New("settingscrypto: value is encrypted but no key is configured (set SETTINGS_ENCRYPTION_KEY)")
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, encPrefix))
	if err != nil {
		return "", fmt.Errorf("settingscrypto: decoding ciphertext: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("settingscrypto: ciphertext too short")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("settingscrypto: decrypting (wrong key?): %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("settingscrypto: building cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settingscrypto: building GCM: %w", err)
	}
	return gcm, nil
}
