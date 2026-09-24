package sqlrepo

import "testing"

// TestVectorShardCount_SplitsOnlyAboveHalfvecLimit is a pure unit test (no
// Postgres needed) for the shard-count math EnableANN/TopSemanticMatches
// rely on: unchanged (1 shard) at and under pgvector's 4000-dim halfvec
// index ceiling, split into just enough even shards above it.
func TestVectorShardCount_SplitsOnlyAboveHalfvecLimit(t *testing.T) {
	cases := map[int]int{
		1:    1,
		128:  1,
		3584: 1,
		4000: 1,
		4001: 2,
		4096: 2,
		8000: 2,
		8001: 3,
	}
	for dims, want := range cases {
		if got := vectorShardCount(dims); got != want {
			t.Errorf("vectorShardCount(%d) = %d, want %d", dims, got, want)
		}
	}
}

// TestVectorShardBounds_SplitsEvenAndHandlesRemainder covers both the
// clean-divide case (4096 -> two 2048-dim shards, exactly what
// Qwen3-VL-Embedding-8B needs) and an uneven one, proving every dimension
// is covered exactly once with no gap or overlap either way.
func TestVectorShardBounds_SplitsEvenAndHandlesRemainder(t *testing.T) {
	bounds := vectorShardBounds(4096, 2)
	want := []int{0, 2048, 4096}
	if len(bounds) != len(want) {
		t.Fatalf("bounds = %v, want %v", bounds, want)
	}
	for i := range want {
		if bounds[i] != want[i] {
			t.Errorf("bounds[%d] = %d, want %d", i, bounds[i], want[i])
		}
	}

	// 4097 dims across 2 shards doesn't divide evenly -- the remainder goes
	// to the earliest shard(s), but every dimension must still be covered
	// exactly once.
	unevenBounds := vectorShardBounds(4097, 2)
	if unevenBounds[0] != 0 || unevenBounds[len(unevenBounds)-1] != 4097 {
		t.Errorf("expected bounds to span the full 4097 dims, got %v", unevenBounds)
	}
	for i := 1; i < len(unevenBounds); i++ {
		if unevenBounds[i] <= unevenBounds[i-1] {
			t.Errorf("expected strictly increasing bounds, got %v", unevenBounds)
		}
	}
}

// TestVectorColumnNameFor_UnsuffixedForSingleShard proves the
// backward-compatibility guarantee vectorColumnNameFor's doc comment
// promises: a provider needing only 1 shard (every provider that existed
// before sharding did, and the overwhelming majority still do) gets the
// exact same unsuffixed column/index names as before sharding existed, so
// no already-deployed single-shard provider's column needs renaming.
func TestVectorColumnNameFor_UnsuffixedForSingleShard(t *testing.T) {
	if got, want := vectorColumnNameFor("hash", 0, 1), "embedding_vector_hash"; got != want {
		t.Errorf("vectorColumnNameFor(hash, 0, 1) = %q, want %q", got, want)
	}
	if got, want := vectorIndexNameFor("hash", 0, 1), "idx_documents_embedding_vector_hash_hnsw"; got != want {
		t.Errorf("vectorIndexNameFor(hash, 0, 1) = %q, want %q", got, want)
	}
}

// TestVectorColumnNameFor_SuffixedPerShardWhenSharded proves a provider
// that does need sharding gets distinct, deterministic per-shard names.
func TestVectorColumnNameFor_SuffixedPerShardWhenSharded(t *testing.T) {
	if got, want := vectorColumnNameFor("qwen3_vl", 0, 2), "embedding_vector_qwen3_vl_0"; got != want {
		t.Errorf("vectorColumnNameFor(qwen3_vl, 0, 2) = %q, want %q", got, want)
	}
	if got, want := vectorColumnNameFor("qwen3_vl", 1, 2), "embedding_vector_qwen3_vl_1"; got != want {
		t.Errorf("vectorColumnNameFor(qwen3_vl, 1, 2) = %q, want %q", got, want)
	}
	if got, want := vectorIndexNameFor("qwen3_vl", 1, 2), "idx_documents_embedding_vector_qwen3_vl_1_hnsw"; got != want {
		t.Errorf("vectorIndexNameFor(qwen3_vl, 1, 2) = %q, want %q", got, want)
	}
}

// TestFormatPgVectorLiteral_FormatsAsPgvectorArraySyntax proves the exact
// "[v1,v2,...]" shape pgvector's own literal syntax requires -- no
// spaces, values formatted with strconv's shortest round-trippable
// representation.
func TestFormatPgVectorLiteral_FormatsAsPgvectorArraySyntax(t *testing.T) {
	if got, want := formatPgVectorLiteral([]float32{0.5, -1, 2.25}), "[0.5,-1,2.25]"; got != want {
		t.Errorf("formatPgVectorLiteral(...) = %q, want %q", got, want)
	}
}

func TestFormatPgVectorLiteral_EmptyVectorIsEmptyBrackets(t *testing.T) {
	if got, want := formatPgVectorLiteral(nil), "[]"; got != want {
		t.Errorf("formatPgVectorLiteral(nil) = %q, want %q", got, want)
	}
}

// TestAnnState_ShardsForDefaultsToOneUntilMarkedAvailable proves a
// provider EnableANN hasn't (yet, or ever) succeeded for gets the
// pre-sharding single-column assumption rather than 0 -- see
// annState.shardsFor's own doc comment for why that matters.
func TestAnnState_ShardsForDefaultsToOneUntilMarkedAvailable(t *testing.T) {
	var a annState
	if got := a.shardsFor("never-marked"); got != 1 {
		t.Errorf("shardsFor on a never-marked provider = %d, want 1", got)
	}
	a.markAvailable("qwen3_vl", 2)
	if got := a.shardsFor("qwen3_vl"); got != 2 {
		t.Errorf("shardsFor after markAvailable(qwen3_vl, 2) = %d, want 2", got)
	}
	if !a.isAvailable("qwen3_vl") {
		t.Error("expected qwen3_vl to be available after markAvailable")
	}
	if a.isAvailable("never-marked") {
		t.Error("expected a never-marked provider to stay unavailable")
	}
}
