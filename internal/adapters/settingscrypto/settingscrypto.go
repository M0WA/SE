// Package settingscrypto encrypts the admin-configured embedding API key
// before it's persisted to app_settings, decrypting it only at
// bootstrap.NewEmbedder. It must stay usable as a real header value, so it
// can't be hashed like a password -- the key lives only in the env file
// (never the DB), so DB-only access alone can't recover the plaintext.
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

// encPrefix marks a value Encrypt produced. Anything else (including a
// pre-existing value, or an unconfigured key) is treated as already
// plaintext by Decrypt, so old data never breaks.
const encPrefix = "enc:v1:"

// keyLen is 32 bytes: AES-256.
const keyLen = 32

// ParseKey decodes a hex-encoded 32-byte key, e.g. SETTINGS_ENCRYPTION_KEY
// (`openssl rand -hex 32`). Empty returns a nil key, no error -- encryption
// is opt-in. A non-32-byte key is a real error, worth failing loudly on.
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

// Encrypt seals plaintext with key (AES-256-GCM, random nonce prepended,
// base64-encoded, prefixed with encPrefix). A nil key or empty plaintext
// returns plaintext unchanged -- the non-breaking, opt-in default.
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
// (predates this feature, or no key configured). A value WITH the prefix
// but no usable key is a real error, surfaced here rather than silently
// returning ciphertext as the real API key.
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
