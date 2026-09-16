package domain_test

import (
	"testing"

	"searchengine/internal/domain"
)

func TestNewEmbeddingEndpointID_SlugifiesName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"IONOS bge-m3", "ionos_bge_m3"},
		{"  leading and trailing  ", "leading_and_trailing"},
		{"Ollama", "ollama"},
		{"Multiple   Spaces", "multiple_spaces"},
	}
	for _, tc := range cases {
		if got := domain.NewEmbeddingEndpointID(tc.name, nil); got != tc.want {
			t.Errorf("NewEmbeddingEndpointID(%q, nil) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNewEmbeddingEndpointID_TruncatesToPatternMaxLength(t *testing.T) {
	got := domain.NewEmbeddingEndpointID("a very long descriptive endpoint name indeed", nil)
	if len(got) > 20 {
		t.Errorf("expected ID truncated to at most 20 chars, got %q (%d chars)", got, len(got))
	}
	if !domain.EmbeddingEndpointIDPattern.MatchString(got) {
		t.Errorf("expected %q to match EmbeddingEndpointIDPattern", got)
	}
}

func TestNewEmbeddingEndpointID_NoAlphanumericFallsBackToTimestamp(t *testing.T) {
	got := domain.NewEmbeddingEndpointID("!!!", nil)
	if got == "" {
		t.Error("expected a non-empty fallback ID for a name with no alphanumeric characters")
	}
	if !domain.EmbeddingEndpointIDPattern.MatchString(got) {
		t.Errorf("expected fallback ID %q to match EmbeddingEndpointIDPattern", got)
	}
}

func TestNewEmbeddingEndpointID_DedupesAgainstExisting(t *testing.T) {
	existing := map[string]bool{"ollama": true}
	got := domain.NewEmbeddingEndpointID("Ollama", existing)
	if got != "ollama_2" {
		t.Errorf("expected the first collision to dedupe to \"ollama_2\", got %q", got)
	}
}

func TestNewEmbeddingEndpointID_DedupesPastMultipleCollisions(t *testing.T) {
	existing := map[string]bool{"ollama": true, "ollama_2": true, "ollama_3": true}
	got := domain.NewEmbeddingEndpointID("Ollama", existing)
	if got != "ollama_4" {
		t.Errorf("expected dedupe to skip past every taken suffix, got %q", got)
	}
}

// TestNewEmbeddingEndpointID_NeverCollidesWithReservedHash proves the
// caller's convention (passing "hash" in existing) actually works: a new
// endpoint literally named "hash" never collides with the built-in
// provider's own reserved ID.
func TestNewEmbeddingEndpointID_NeverCollidesWithReservedHash(t *testing.T) {
	existing := map[string]bool{domain.EmbeddingProviderHash: true}
	got := domain.NewEmbeddingEndpointID("hash", existing)
	if got == domain.EmbeddingProviderHash {
		t.Errorf("expected a new endpoint named %q to not collide with the reserved hash provider ID, got %q", "hash", got)
	}
}

func TestReconcileActiveProvider_HashActiveAndEnabledStays(t *testing.T) {
	got := domain.ReconcileActiveProvider(domain.EmbeddingProviderHash, true, nil)
	if got != domain.EmbeddingProviderHash {
		t.Errorf("expected hash to stay active when enabled, got %q", got)
	}
}

func TestReconcileActiveProvider_HashActiveButDisabledFallsBackToEnabledEndpoint(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: true}}
	got := domain.ReconcileActiveProvider(domain.EmbeddingProviderHash, false, endpoints)
	if got != "ionos" {
		t.Errorf("expected fallback to the one enabled endpoint, got %q", got)
	}
}

func TestReconcileActiveProvider_EnabledEndpointStaysActive(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: true}}
	got := domain.ReconcileActiveProvider("ionos", true, endpoints)
	if got != "ionos" {
		t.Errorf("expected the enabled endpoint to stay active even with hash also enabled, got %q", got)
	}
}

func TestReconcileActiveProvider_DisabledEndpointFallsBackToHash(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: false}}
	got := domain.ReconcileActiveProvider("ionos", true, endpoints)
	if got != domain.EmbeddingProviderHash {
		t.Errorf("expected a disabled endpoint to self-heal to hash, got %q", got)
	}
}

func TestReconcileActiveProvider_DeletedEndpointFallsBackToHash(t *testing.T) {
	got := domain.ReconcileActiveProvider("some-deleted-id", true, nil)
	if got != domain.EmbeddingProviderHash {
		t.Errorf("expected a deleted/unknown endpoint ID to self-heal to hash, got %q", got)
	}
}

func TestReconcileActiveProvider_DeletedEndpointFallsBackToFirstEnabledWhenHashDisabled(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "a", Enabled: false}, {ID: "b", Enabled: true}}
	got := domain.ReconcileActiveProvider("some-deleted-id", false, endpoints)
	if got != "b" {
		t.Errorf("expected fallback to the first enabled endpoint, got %q", got)
	}
}

// TestReconcileActiveProvider_NothingEnabledFallsBackToHashAnyway proves the
// degenerate case (hash disabled, no endpoint enabled) still returns a
// non-empty, well-known value rather than an empty string -- search finds
// nothing either way, but callers never have to special-case "" downstream.
func TestReconcileActiveProvider_NothingEnabledFallsBackToHashAnyway(t *testing.T) {
	got := domain.ReconcileActiveProvider("anything", false, nil)
	if got != domain.EmbeddingProviderHash {
		t.Errorf("expected the degenerate nothing-enabled case to still return hash, got %q", got)
	}
}
