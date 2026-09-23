package sqlrepo_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// packFloat32sLE and unpackFloat32sLE are a self-contained (no dependency on
// sqlrepo's own production code) little-endian packed-binary codec for
// []float32, deliberately kept local to this benchmark file rather than
// calling into sqlrepo's internals -- so this JSON-vs-packed-binary
// comparison stays stable even if the production codec (EncodeEmbedding/
// DecodeEmbedding in embedding_codec.go, the codec documents.embedding
// actually uses today) changes shape later.
func packFloat32sLE(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func unpackFloat32sLE(data []byte) []float32 {
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}

// randomEmbedding returns a deterministic realistic-looking embedding
// vector of the given dimensionality.
func randomEmbedding(rng *rand.Rand, dims int) []float32 {
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = rng.Float32()*2 - 1
	}
	return vec
}

// BenchmarkEmbeddingCodec is a pure-Go (no DB) comparison of json.Marshal/
// Unmarshal against a packed little-endian binary codec for a realistic
// []float32 embedding, at two pool sizes: 200 (SemanticCandidatePoolSize's
// real default, see hybrid_search_service.go) and 5,000 (this package's
// larger-corpus benchmark convention, e.g. BenchmarkDocumentFetch/
// BenchmarkPostingsFetch's corpusSize). Dimensionality is 128, this
// codebase's actual embedder default (hashembed.New(128), see
// cmd/search/main.go/cmd/admin/main.go/cmd/crawl/main.go) -- not the
// higher dimensionality of some real-world embedding models, which this
// codebase doesn't use.
//
// This isolates the pure codec/format cost (reflection-driven JSON token
// scanning and strconv per float vs. a fixed 4-byte store/load per float);
// BenchmarkSampleEmbeddings (repository_bench_test.go) instead measures the
// same trade-off end-to-end through a real SQLite-backed documents table
// (row size, I/O, and codec cost together).
func BenchmarkEmbeddingCodec(b *testing.B) {
	const dims = 128
	rng := rand.New(rand.NewSource(42))

	for _, poolSize := range []int{200, 5000} {
		vecs := make([][]float32, poolSize)
		for i := range vecs {
			vecs[i] = randomEmbedding(rng, dims)
		}

		jsonBlobs := make([][]byte, poolSize)
		for i, v := range vecs {
			blob, err := json.Marshal(v)
			if err != nil {
				b.Fatalf("json.Marshal: %v", err)
			}
			jsonBlobs[i] = blob
		}
		binBlobs := make([][]byte, poolSize)
		for i, v := range vecs {
			binBlobs[i] = packFloat32sLE(v)
		}

		b.Run(newCodecBenchName("JSON_Marshal", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, v := range vecs {
					if _, err := json.Marshal(v); err != nil {
						b.Fatalf("json.Marshal: %v", err)
					}
				}
			}
		})
		b.Run(newCodecBenchName("JSON_Unmarshal", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, blob := range jsonBlobs {
					var v []float32
					if err := json.Unmarshal(blob, &v); err != nil {
						b.Fatalf("json.Unmarshal: %v", err)
					}
				}
			}
		})
		b.Run(newCodecBenchName("Binary_Pack", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, v := range vecs {
					_ = packFloat32sLE(v)
				}
			}
		})
		b.Run(newCodecBenchName("Binary_Unpack", poolSize), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, blob := range binBlobs {
					_ = unpackFloat32sLE(blob)
				}
			}
		})
	}
}

func newCodecBenchName(op string, poolSize int) string {
	return fmt.Sprintf("%s/PoolSize=%d", op, poolSize)
}
