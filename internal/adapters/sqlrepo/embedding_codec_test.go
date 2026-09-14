package sqlrepo_test

import (
	"testing"

	"searchengine/internal/adapters/sqlrepo"
)

func TestEncodeDecodeEmbedding_RoundTrips(t *testing.T) {
	vec := []float32{0.1, -2.5, 3.0, 0}
	blob := sqlrepo.EncodeEmbedding(vec)
	if len(blob) != len(vec)*4 {
		t.Fatalf("expected a %d-byte blob, got %d", len(vec)*4, len(blob))
	}
	got, err := sqlrepo.DecodeEmbedding(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("expected %d floats back, got %d", len(vec), len(got))
	}
	for i, v := range vec {
		if got[i] != v {
			t.Errorf("index %d: expected %v, got %v", i, v, got[i])
		}
	}
}

func TestEncodeDecodeEmbedding_EmptyVectorRoundTrips(t *testing.T) {
	blob := sqlrepo.EncodeEmbedding(nil)
	if len(blob) != 0 {
		t.Fatalf("expected an empty blob for a nil vector, got %d bytes", len(blob))
	}
	got, err := sqlrepo.DecodeEmbedding(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected an empty vector back, got %v", got)
	}
}

func TestDecodeEmbedding_InvalidLengthErrors(t *testing.T) {
	if _, err := sqlrepo.DecodeEmbedding([]byte{1, 2, 3}); err == nil {
		t.Error("expected an error decoding a blob whose length isn't a multiple of 4")
	}
}
