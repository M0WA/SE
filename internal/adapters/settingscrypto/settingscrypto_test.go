package settingscrypto_test

import (
	"crypto/rand"
	"errors"
	"io"
	"strings"
	"testing"

	"searchengine/internal/adapters/settingscrypto"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := settingscrypto.ParseKey("abababababababababababababababababababababababababababababababab")
	if err != nil {
		t.Fatalf("unexpected error parsing test key: %v", err)
	}
	return key
}

func TestParseKey_Empty(t *testing.T) {
	key, err := settingscrypto.ParseKey("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected a nil key for an empty input, got %v", key)
	}
}

func TestParseKey_InvalidHex(t *testing.T) {
	if _, err := settingscrypto.ParseKey("not-hex-zz"); err == nil {
		t.Error("expected an error for invalid hex")
	}
}

func TestParseKey_WrongLength(t *testing.T) {
	if _, err := settingscrypto.ParseKey("abcd"); err == nil {
		t.Error("expected an error for a key that isn't 32 bytes")
	}
}

func TestParseKey_ValidHex32Bytes(t *testing.T) {
	key := testKey(t)
	if len(key) != 32 {
		t.Errorf("expected a 32-byte key, got %d bytes", len(key))
	}
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := testKey(t)
	enc, err := settingscrypto.Encrypt(key, "sk-super-secret-api-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if enc == "sk-super-secret-api-key" {
		t.Error("expected the value to actually be encrypted, not passed through")
	}
	if !strings.HasPrefix(enc, "enc:v1:") {
		t.Errorf("expected the enc:v1: prefix, got %q", enc)
	}
	dec, err := settingscrypto.Decrypt(key, enc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec != "sk-super-secret-api-key" {
		t.Errorf("expected round-trip to recover the plaintext, got %q", dec)
	}
}

func TestEncrypt_ProducesDifferentCiphertextEachTime(t *testing.T) {
	key := testKey(t)
	a, err := settingscrypto.Encrypt(key, "same-plaintext")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := settingscrypto.Encrypt(key, "same-plaintext")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Error("expected two encryptions of the same plaintext to differ (random nonce)")
	}
}

func TestEncrypt_NilKeyPassesThrough(t *testing.T) {
	got, err := settingscrypto.Encrypt(nil, "plaintext-api-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "plaintext-api-key" {
		t.Errorf("expected the value unchanged with no key configured, got %q", got)
	}
}

func TestEncrypt_EmptyPlaintextPassesThrough(t *testing.T) {
	key := testKey(t)
	got, err := settingscrypto.Encrypt(key, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected an empty value to stay empty, got %q", got)
	}
}

func TestDecrypt_UnprefixedValuePassesThrough(t *testing.T) {
	key := testKey(t)
	got, err := settingscrypto.Decrypt(key, "already-plaintext-value")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "already-plaintext-value" {
		t.Errorf("expected an unprefixed value unchanged, got %q", got)
	}
}

func TestDecrypt_EmptyValuePassesThrough(t *testing.T) {
	if got, err := settingscrypto.Decrypt(nil, ""); err != nil || got != "" {
		t.Errorf("expected empty in, empty out, no error; got %q, %v", got, err)
	}
}

func TestDecrypt_EncryptedValueWithNoKeyErrors(t *testing.T) {
	key := testKey(t)
	enc, err := settingscrypto.Encrypt(key, "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := settingscrypto.Decrypt(nil, enc); err == nil {
		t.Error("expected an error decrypting an encrypted value with no key configured")
	}
}

func TestDecrypt_WrongKeyErrors(t *testing.T) {
	key := testKey(t)
	enc, err := settingscrypto.Encrypt(key, "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wrongKey, err := settingscrypto.ParseKey("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if err != nil {
		t.Fatalf("unexpected error parsing wrong key: %v", err)
	}
	if _, err := settingscrypto.Decrypt(wrongKey, enc); err == nil {
		t.Error("expected an error decrypting with the wrong key")
	}
}

func TestDecrypt_MalformedBase64Errors(t *testing.T) {
	key := testKey(t)
	if _, err := settingscrypto.Decrypt(key, "enc:v1:not-valid-base64!!!"); err == nil {
		t.Error("expected an error for malformed base64")
	}
}

func TestDecrypt_TooShortCiphertextErrors(t *testing.T) {
	key := testKey(t)
	// "enc:v1:" + base64("x") -- far shorter than a valid nonce+ciphertext.
	if _, err := settingscrypto.Decrypt(key, "enc:v1:eA=="); err == nil {
		t.Error("expected an error for ciphertext too short to contain a nonce")
	}
}

// TestEncrypt_BadKeySizeErrors and TestDecrypt_BadKeySizeErrors exercise
// newGCM's own error path directly -- unreachable through ParseKey (which
// only ever hands out a nil or exactly-32-byte key), but Encrypt/Decrypt
// take a plain []byte, not a ParseKey-branded type, so nothing stops a
// caller from passing a malformed one.
func TestEncrypt_BadKeySizeErrors(t *testing.T) {
	if _, err := settingscrypto.Encrypt([]byte("too-short"), "plaintext"); err == nil {
		t.Error("expected an error for a key that isn't a valid AES key size")
	}
}

func TestDecrypt_BadKeySizeErrors(t *testing.T) {
	if _, err := settingscrypto.Decrypt([]byte("too-short"), "enc:v1:eA=="); err == nil {
		t.Error("expected an error for a key that isn't a valid AES key size")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// TestEncrypt_NonceGenerationFailureErrors swaps crypto/rand.Reader (a
// package-level var, restored via defer) to prove Encrypt surfaces a
// nonce-generation failure rather than silently sealing with a zero/short
// nonce.
func TestEncrypt_NonceGenerationFailureErrors(t *testing.T) {
	key := testKey(t)
	orig := rand.Reader
	rand.Reader = failingReader{}
	defer func() { rand.Reader = orig }()

	if _, err := settingscrypto.Encrypt(key, "plaintext"); err == nil {
		t.Error("expected an error when nonce generation fails")
	}
}

var _ io.Reader = failingReader{}
