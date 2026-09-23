package restapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"searchengine/internal/adapters/restapi"
	"searchengine/internal/domain"
	"searchengine/internal/ports"
)

// fakeSemanticMatcher is a minimal ports.SemanticMatcher fake.
type fakeSemanticMatcher struct {
	matches map[string]domain.EmbeddedVector
	ok      bool
	err     error
	// gotProvider/gotLimit record the last call's arguments, for tests
	// asserting the handler passed them through correctly.
	gotProvider string
	gotLimit    int
}

func (f *fakeSemanticMatcher) TopSemanticMatches(_ context.Context, _ []float32, limit int, provider string) (map[string]domain.EmbeddedVector, bool, error) {
	f.gotProvider = provider
	f.gotLimit = limit
	if f.err != nil {
		return nil, false, f.err
	}
	return f.matches, f.ok, nil
}

// fakeImageEmbeddingProvider implements both ports.EmbeddingProvider and
// ports.ImageEmbedder -- a provider that supports image embedding.
type fakeImageEmbeddingProvider struct {
	vec    []float32
	err    error
	gotB64 string
}

func (f *fakeImageEmbeddingProvider) Embed(context.Context, string) ([]float32, error) {
	return f.vec, f.err
}
func (f *fakeImageEmbeddingProvider) Dimensions() int { return len(f.vec) }
func (f *fakeImageEmbeddingProvider) EmbedImage(_ context.Context, base64Data, _ string) ([]float32, error) {
	f.gotB64 = base64Data
	return f.vec, f.err
}

func visionSimilarityConfig(t *testing.T, matcher ports.SemanticMatcher, embeddingRepo ports.EmbeddingRepository, embedders map[string]ports.EmbeddingProvider, key string) *restapi.Handler {
	t.Helper()
	return restapi.New(restapi.Config{
		SemanticMatcher: matcher, EmbeddingRepo: embeddingRepo, Embedders: embedders,
		InternalVisionAPIKey: key, DBDriver: "pgx",
	})
}

func postVisionSimilarity(h *restapi.Handler, key string, body map[string]interface{}) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/search/api/vision-similarity", bytes.NewReader(data))
	if key != "" {
		req.Header.Set("X-Internal-API-Key", key)
	}
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	return rec
}

func TestHandleVisionSimilarity_MethodNotAllowed(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, nil, "secret")
	req := httptest.NewRequest(http.MethodGet, "/search/api/vision-similarity", nil)
	req.Header.Set("X-Internal-API-Key", "secret")
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_WrongKeyRejected(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, nil, "secret")
	rec := postVisionSimilarity(h, "wrong-key", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_NoKeyConfiguredRejectsEveryCall(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, nil, "")
	rec := postVisionSimilarity(h, "anything", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no key is configured at all, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_NotConfigured(t *testing.T) {
	h := visionSimilarityConfig(t, nil, nil, nil, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_InvalidJSONRejected(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": &fakeImageEmbeddingProvider{}}, "secret")
	req := httptest.NewRequest(http.MethodPost, "/search/api/vision-similarity", bytes.NewReader([]byte("not json")))
	req.Header.Set("X-Internal-API-Key", "secret")
	rec := httptest.NewRecorder()
	h.RoutesSearch().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_EmptyBase64Rejected(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": &fakeImageEmbeddingProvider{}}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_UnknownProviderRejected(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"other": &fakeImageEmbeddingProvider{}}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "unknown", "base64": "QUJD"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// TestHandleVisionSimilarity_ProviderWithoutImageEmbedderRejected proves a
// provider that only implements plain-text Embed (e.g. the built-in hash
// provider) is rejected with a clear message, not a panic/type-assertion crash.
type textOnlyProvider struct{}

func (textOnlyProvider) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func (textOnlyProvider) Dimensions() int                                  { return 1 }

func TestHandleVisionSimilarity_ProviderWithoutImageEmbedderRejected(t *testing.T) {
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": textOnlyProvider{}}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_EmbedImageErrorReturnsBadGateway(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{err: errors.New("upstream exploded")}
	h := visionSimilarityConfig(t, &fakeSemanticMatcher{}, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", rec.Code)
	}
}

func TestHandleVisionSimilarity_ANNUnavailableReturns503(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1, 0}}
	matcher := &fakeSemanticMatcher{ok: false}
	h := visionSimilarityConfig(t, matcher, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleVisionSimilarity_SemanticMatcherErrorReturns500(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1, 0}}
	matcher := &fakeSemanticMatcher{err: errors.New("query failed")}
	h := visionSimilarityConfig(t, matcher, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// TestHandleVisionSimilarity_Success proves the whole pipeline: embed via
// the provider, rank ANN candidates by cosine similarity (not just return
// them in map order), resolve titles/URLs, and cap at the requested limit.
func TestHandleVisionSimilarity_Success(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1, 0}}
	matcher := &fakeSemanticMatcher{
		ok: true,
		matches: map[string]domain.EmbeddedVector{
			"a": {Vector: []float32{1, 0}},     // identical -> similarity 1.0
			"b": {Vector: []float32{0, 1}},     // orthogonal -> similarity 0.0
			"c": {Vector: []float32{0.7, 0.7}}, // similarity ~0.7
		},
	}
	repo := &fakeEmbeddingRepo{docs: map[string]domain.Document{
		"a": {ID: "a", URL: "https://a.example", Title: "Doc A"},
		"b": {ID: "b", URL: "https://b.example", Title: "Doc B"},
		"c": {ID: "c", URL: "https://c.example", Title: "Doc C"},
	}}
	h := visionSimilarityConfig(t, matcher, repo, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")

	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD", "limit": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if embedder.gotB64 != "QUJD" {
		t.Errorf("expected the request's base64 image forwarded to EmbedImage, got %q", embedder.gotB64)
	}
	var resp struct {
		Matches []struct {
			URL   string  `json:"url"`
			Title string  `json:"title"`
			Score float64 `json:"score"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Matches) != 2 {
		t.Fatalf("expected exactly 2 matches (limit=2), got %+v", resp.Matches)
	}
	if resp.Matches[0].Title != "Doc A" || resp.Matches[0].Score < 0.99 {
		t.Errorf("expected the identical vector ranked first with score ~1.0, got %+v", resp.Matches[0])
	}
	if resp.Matches[1].Title != "Doc C" {
		t.Errorf("expected the second-most-similar doc ranked second, got %+v", resp.Matches[1])
	}
}

func TestHandleVisionSimilarity_DefaultLimitIsFive(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1}}
	matcher := &fakeSemanticMatcher{ok: true, matches: map[string]domain.EmbeddedVector{}}
	h := visionSimilarityConfig(t, matcher, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if matcher.gotLimit != 5*10 {
		t.Errorf("expected the default limit (5) times the ANN pool multiplier (10) = 50, got %d", matcher.gotLimit)
	}
}

func TestHandleVisionSimilarity_LimitClampedToMax(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1}}
	matcher := &fakeSemanticMatcher{ok: true, matches: map[string]domain.EmbeddedVector{}}
	h := visionSimilarityConfig(t, matcher, &fakeEmbeddingRepo{}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD", "limit": 1000})
	if matcher.gotLimit != 20*10 {
		t.Errorf("expected the limit clamped to the max (20) times the ANN pool multiplier (10) = 200, got %d", matcher.gotLimit)
	}
}

// TestHandleVisionSimilarity_DocumentsByIDsErrorReturnsEmptyMatches proves
// rankVisionMatches degrades to no matches (not a crash/500) if resolving
// titles fails after a successful ANN search.
func TestHandleVisionSimilarity_DocumentsByIDsErrorReturnsEmptyMatches(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1}}
	matcher := &fakeSemanticMatcher{ok: true, matches: map[string]domain.EmbeddedVector{"a": {Vector: []float32{1}}}}
	repo := &fakeEmbeddingRepo{documentsByIDsErr: errors.New("db unavailable")}
	h := visionSimilarityConfig(t, matcher, repo, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (degrades gracefully), got %d", rec.Code)
	}
	var resp struct {
		Matches []struct{} `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Matches != nil {
		t.Errorf("expected no matches when DocumentsByIDs fails, got %+v", resp.Matches)
	}
}

// TestHandleVisionSimilarity_DeletedDocumentSkipped proves a doc id
// present in the ANN candidate pool but absent from DocumentsByIDs
// (deleted in the meantime) is silently skipped, not an error.
func TestHandleVisionSimilarity_DeletedDocumentSkipped(t *testing.T) {
	embedder := &fakeImageEmbeddingProvider{vec: []float32{1}}
	matcher := &fakeSemanticMatcher{ok: true, matches: map[string]domain.EmbeddedVector{
		"gone": {Vector: []float32{1}},
	}}
	h := visionSimilarityConfig(t, matcher, &fakeEmbeddingRepo{docs: map[string]domain.Document{}}, map[string]ports.EmbeddingProvider{"p": embedder}, "secret")
	rec := postVisionSimilarity(h, "secret", map[string]interface{}{"provider": "p", "base64": "QUJD"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Matches []struct{} `json:"matches"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(resp.Matches) != 0 {
		t.Errorf("expected the deleted document silently skipped, got %+v", resp.Matches)
	}
}
