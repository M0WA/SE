package bootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"searchengine/internal/adapters/hashembed"
	"searchengine/internal/adapters/httpembed"
	"searchengine/internal/adapters/settingscrypto"
	"searchengine/internal/bootstrap"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

func TestNewHTTPEmbedder_BuildsHTTPEmbedder(t *testing.T) {
	e := bootstrap.NewHTTPEmbedder(domain.EmbeddingHTTPEndpoint{
		BaseURL: "http://localhost:11434/v1", APIKey: "sk-test",
		Model: "nomic-embed-text", Dimensions: 768,
	})
	he, ok := e.(*httpembed.Embedder)
	if !ok {
		t.Fatalf("expected *httpembed.Embedder, got %T", e)
	}
	if he.Dimensions() != 768 {
		t.Errorf("expected 768 dimensions, got %d", he.Dimensions())
	}
}

// TestNewHTTPEmbedder_WiresChunkSizeTokens proves ChunkSizeTokens actually
// reaches the constructed httpembed.Embedder's Config -- httpembed has no
// exported getter for it (unlike Dimensions, which EnableANN's own
// EmbedderDimensions needs in production), so this checks the effect
// behaviorally: a small chunk budget against multi-word text must produce
// more than one embeddings call.
func TestNewHTTPEmbedder_WiresChunkSizeTokens(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	e := bootstrap.NewHTTPEmbedder(domain.EmbeddingHTTPEndpoint{
		BaseURL: srv.URL, Dimensions: 1, ChunkSizeTokens: 2,
	})
	if _, err := e.Embed(context.Background(), "aaaaaaaaaa bbbbbbbbbb cccccccccc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls <= 1 {
		t.Errorf("expected ChunkSizeTokens to split this text into multiple embeddings calls, got %d", calls)
	}
}

// TestNewHTTPEmbedder_WiresTokenizeURL proves TokenizeURL reaches the
// constructed Embedder the same way -- a configured TokenizeURL must
// actually get called during chunking.
func TestNewHTTPEmbedder_WiresTokenizeURL(t *testing.T) {
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer embedSrv.Close()

	var tokenizeCalled bool
	tokenizeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenizeCalled = true
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"count": 1})
	}))
	defer tokenizeSrv.Close()

	e := bootstrap.NewHTTPEmbedder(domain.EmbeddingHTTPEndpoint{
		BaseURL: embedSrv.URL, Dimensions: 1, ChunkSizeTokens: 2, TokenizeURL: tokenizeSrv.URL,
	})
	if _, err := e.Embed(context.Background(), "aa bb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !tokenizeCalled {
		t.Error("expected the configured TokenizeURL to be called during chunking")
	}
}

func TestNewEmbedders_HashOnlyByDefault(t *testing.T) {
	embedders := bootstrap.NewEmbedders(true, nil)
	if len(embedders) != 1 {
		t.Fatalf("expected exactly one embedder, got %d: %+v", len(embedders), embedders)
	}
	if _, ok := embedders[domain.EmbeddingProviderHash].(*hashembed.Embedder); !ok {
		t.Errorf("expected a *hashembed.Embedder under %q, got %+v", domain.EmbeddingProviderHash, embedders)
	}
}

func TestNewEmbedders_NeitherEnabledReturnsEmptyMap(t *testing.T) {
	embedders := bootstrap.NewEmbedders(false, nil)
	if len(embedders) != 0 {
		t.Errorf("expected no embedders when nothing is enabled, got %+v", embedders)
	}
}

func TestNewEmbedders_OnlyEnabledEndpointsIncluded(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{
		{ID: "enabled-one", BaseURL: "http://localhost:11434/v1", Dimensions: 768, Enabled: true},
		{ID: "disabled-one", BaseURL: "http://localhost:11435/v1", Dimensions: 512, Enabled: false},
	}
	embedders := bootstrap.NewEmbedders(false, endpoints)
	if len(embedders) != 1 {
		t.Fatalf("expected exactly one embedder, got %d: %+v", len(embedders), embedders)
	}
	he, ok := embedders["enabled-one"].(*httpembed.Embedder)
	if !ok {
		t.Fatalf("expected a *httpembed.Embedder under %q, got %+v", "enabled-one", embedders)
	}
	if he.Dimensions() != 768 {
		t.Errorf("expected 768 dimensions, got %d", he.Dimensions())
	}
	if _, ok := embedders["disabled-one"]; ok {
		t.Error("expected the disabled endpoint to be absent")
	}
}

func TestNewEmbedders_HashPlusMultipleEndpoints(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{
		{ID: "a", BaseURL: "http://a", Dimensions: 768, Enabled: true},
		{ID: "b", BaseURL: "http://b", Dimensions: 512, Enabled: true},
	}
	embedders := bootstrap.NewEmbedders(true, endpoints)
	if len(embedders) != 3 {
		t.Fatalf("expected hash + both endpoints, got %d: %+v", len(embedders), embedders)
	}
	for _, id := range []string{domain.EmbeddingProviderHash, "a", "b"} {
		if _, ok := embedders[id]; !ok {
			t.Errorf("expected provider %q present, got %+v", id, embedders)
		}
	}
}

func TestEmbedderDimensions_ReflectsEachEmbeddersOwnDimensions(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "http", BaseURL: "http://localhost:11434/v1", Dimensions: 768, Enabled: true}}
	embedders := bootstrap.NewEmbedders(true, endpoints)
	dims := bootstrap.EmbedderDimensions(embedders)
	if dims[domain.EmbeddingProviderHash] != 128 {
		t.Errorf("expected hash dims=128, got %+v", dims)
	}
	if dims["http"] != 768 {
		t.Errorf("expected http dims=768, got %+v", dims)
	}
}

func TestEmbedderDimensions_EmptyMapForEmptyEmbedders(t *testing.T) {
	dims := bootstrap.EmbedderDimensions(map[string]ports.EmbeddingProvider{})
	if len(dims) != 0 {
		t.Errorf("expected an empty map, got %+v", dims)
	}
}

// TestNewHTTPEmbedder_WiresRateLimitPerSecond proves RateLimitPerSecond
// actually reaches the constructed httpembed.Embedder's Config -- like
// ChunkSizeTokens/TokenizeURL, httpembed exposes no getter for it, so this
// checks the effect behaviorally: with a small rate limit and multiple
// chunks forced by a small ChunkSizeTokens, consecutive embeddings
// requests must be measurably paced apart.
func TestNewHTTPEmbedder_WiresRateLimitPerSecond(t *testing.T) {
	var arrivals []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrivals = append(arrivals, time.Now())
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{{"embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	e := bootstrap.NewHTTPEmbedder(domain.EmbeddingHTTPEndpoint{
		BaseURL: srv.URL, Dimensions: 1, ChunkSizeTokens: 2, RateLimitPerSecond: 20, // 50ms/call
	})
	if _, err := e.Embed(context.Background(), "aaaaa bbbbb ccccc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(arrivals) != 3 {
		t.Fatalf("expected 3 chunked embeddings requests, got %d", len(arrivals))
	}
	if gap := arrivals[1].Sub(arrivals[0]); gap < 40*time.Millisecond {
		t.Errorf("expected requests paced at least ~50ms apart, got %v", gap)
	}
}

func TestDecryptEndpointAPIKey_NilKeyPassesPlaintextThrough(t *testing.T) {
	e := bootstrap.DecryptEndpointAPIKey(domain.EmbeddingHTTPEndpoint{APIKey: "sk-plain"}, nil)
	if e.APIKey != "sk-plain" {
		t.Errorf("expected the plaintext key unchanged, got %q", e.APIKey)
	}
}

func TestDecryptEndpointAPIKey_DecryptsAnEncryptedValue(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	e := bootstrap.DecryptEndpointAPIKey(domain.EmbeddingHTTPEndpoint{APIKey: enc}, key)
	if e.APIKey != "sk-real-secret" {
		t.Errorf("expected the decrypted key, got %q", e.APIKey)
	}
}

func TestDecryptEndpointAPIKey_FailureClearsTheKeyRatherThanLeakingCiphertext(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Decrypting with no key configured, even though the stored value IS
	// encrypted, must not hand the raw ciphertext to NewHTTPEmbedder as if
	// it were a usable API key.
	e := bootstrap.DecryptEndpointAPIKey(domain.EmbeddingHTTPEndpoint{APIKey: enc}, nil)
	if e.APIKey != "" {
		t.Errorf("expected the key cleared on a decryption failure, got %q", e.APIKey)
	}
}

func TestDecryptEndpointAPIKeys_AppliesToEveryEndpoint(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "a", APIKey: "sk-a"}, {ID: "b", APIKey: "sk-b"}}
	out := bootstrap.DecryptEndpointAPIKeys(endpoints, nil)
	if len(out) != 2 || out[0].APIKey != "sk-a" || out[1].APIKey != "sk-b" {
		t.Errorf("expected both endpoints' keys passed through unchanged (nil key), got %+v", out)
	}
}

// fakeEmbeddingEndpointStore backs TestLoadEmbeddingEndpoints_* below --
// only ListEmbeddingEndpoints is exercised, the rest of
// ports.EmbeddingEndpointStore is unused by LoadEmbeddingEndpoints itself.
type fakeEmbeddingEndpointStore struct {
	endpoints []domain.EmbeddingHTTPEndpoint
	listErr   error
}

func (f *fakeEmbeddingEndpointStore) CreateEmbeddingEndpoint(context.Context, domain.EmbeddingHTTPEndpoint) error {
	return nil
}
func (f *fakeEmbeddingEndpointStore) GetEmbeddingEndpoint(context.Context, string) (domain.EmbeddingHTTPEndpoint, error) {
	return domain.EmbeddingHTTPEndpoint{}, nil
}
func (f *fakeEmbeddingEndpointStore) ListEmbeddingEndpoints(context.Context) ([]domain.EmbeddingHTTPEndpoint, error) {
	return f.endpoints, f.listErr
}
func (f *fakeEmbeddingEndpointStore) UpdateEmbeddingEndpoint(context.Context, domain.EmbeddingHTTPEndpoint) error {
	return nil
}
func (f *fakeEmbeddingEndpointStore) DeleteEmbeddingEndpoint(context.Context, string) error {
	return nil
}

// TestLoadEmbeddingEndpoints_DecryptsEveryStoredEndpoint proves
// LoadEmbeddingEndpoints is DecryptEndpointAPIKeys applied to whatever
// ListEmbeddingEndpoints returns, not just a passthrough.
func TestLoadEmbeddingEndpoints_DecryptsEveryStoredEndpoint(t *testing.T) {
	key, err := settingscrypto.ParseKey(hex64())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	enc, err := settingscrypto.Encrypt(key, "sk-real")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	store := &fakeEmbeddingEndpointStore{endpoints: []domain.EmbeddingHTTPEndpoint{{ID: "a", APIKey: enc}}}

	got := bootstrap.LoadEmbeddingEndpoints(context.Background(), store, key)
	if len(got) != 1 || got[0].APIKey != "sk-real" {
		t.Errorf("expected the stored endpoint back with its key decrypted, got %+v", got)
	}
}

// TestLoadEmbeddingEndpoints_StoreErrorReturnsNilNotFatal proves a store
// failure degrades to an empty embedder set rather than failing startup
// -- matching every other best-effort SyncX call in this package.
func TestLoadEmbeddingEndpoints_StoreErrorReturnsNilNotFatal(t *testing.T) {
	store := &fakeEmbeddingEndpointStore{listErr: errors.New("db unavailable")}
	got := bootstrap.LoadEmbeddingEndpoints(context.Background(), store, nil)
	if got != nil {
		t.Errorf("expected nil endpoints on a store error, got %+v", got)
	}
}

// hex64 returns a syntactically valid 64-character hex string for
// settingscrypto.ParseKey, without hardcoding the same literal in every
// test above.
func hex64() string {
	const digits = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = digits[i%16]
	}
	return string(b)
}
