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

func TestReconcileSearchWeights_HashWeightedAndEnabledSurvives(t *testing.T) {
	got := domain.ReconcileSearchWeights(map[string]float64{domain.EmbeddingProviderHash: 1}, true, nil)
	if got[domain.EmbeddingProviderHash] != 1 || len(got) != 1 {
		t.Errorf("expected hash's weight to survive unchanged, got %+v", got)
	}
}

func TestReconcileSearchWeights_MultipleEnabledProvidersAllSurvive(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: true}, {ID: "local", Enabled: true}}
	weights := map[string]float64{domain.EmbeddingProviderHash: 0.5, "ionos": 0.3, "local": 0.2}
	got := domain.ReconcileSearchWeights(weights, true, endpoints)
	if len(got) != 3 || got[domain.EmbeddingProviderHash] != 0.5 || got["ionos"] != 0.3 || got["local"] != 0.2 {
		t.Errorf("expected every enabled provider's weight preserved, got %+v", got)
	}
}

func TestReconcileSearchWeights_DisabledEntryDropped(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: false}}
	weights := map[string]float64{domain.EmbeddingProviderHash: 0.5, "ionos": 0.5}
	got := domain.ReconcileSearchWeights(weights, true, endpoints)
	if len(got) != 1 || got[domain.EmbeddingProviderHash] != 0.5 {
		t.Errorf("expected the disabled endpoint's weight dropped, hash's preserved, got %+v", got)
	}
}

func TestReconcileSearchWeights_HashDisabledEntryDropped(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "ionos", Enabled: true}}
	weights := map[string]float64{domain.EmbeddingProviderHash: 0.5, "ionos": 0.5}
	got := domain.ReconcileSearchWeights(weights, false, endpoints)
	if len(got) != 1 || got["ionos"] != 0.5 {
		t.Errorf("expected hash's weight dropped once hash is disabled, ionos's preserved, got %+v", got)
	}
}

func TestReconcileSearchWeights_EmptyFallsBackToHashWhenEnabled(t *testing.T) {
	got := domain.ReconcileSearchWeights(nil, true, nil)
	if len(got) != 1 || got[domain.EmbeddingProviderHash] != 1 {
		t.Errorf("expected fallback to {hash: 1}, got %+v", got)
	}
}

func TestReconcileSearchWeights_EveryEntryDroppedFallsBackToFirstEnabledEndpoint(t *testing.T) {
	endpoints := []domain.EmbeddingHTTPEndpoint{{ID: "a", Enabled: false}, {ID: "b", Enabled: true}}
	// Names only disabled/deleted providers -- nothing survives reconciliation.
	weights := map[string]float64{"some-deleted-id": 1, "a": 1}
	got := domain.ReconcileSearchWeights(weights, false, endpoints)
	if len(got) != 1 || got["b"] != 1 {
		t.Errorf("expected fallback to {b: 1}, got %+v", got)
	}
}

// TestReconcileSearchWeights_NothingEnabledFallsBackToHashAnyway proves the
// degenerate case (hash disabled, no endpoint enabled) still returns a
// non-empty, well-known map rather than one that's empty -- search finds
// nothing either way, but callers never have to special-case an empty map
// downstream.
func TestReconcileSearchWeights_NothingEnabledFallsBackToHashAnyway(t *testing.T) {
	got := domain.ReconcileSearchWeights(map[string]float64{"anything": 1}, false, nil)
	if len(got) != 1 || got[domain.EmbeddingProviderHash] != 1 {
		t.Errorf("expected the degenerate nothing-enabled case to still return {hash: 1}, got %+v", got)
	}
}
