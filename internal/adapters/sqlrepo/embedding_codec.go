package sqlrepo

import (
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeEmbedding packs vec as a sequence of little-endian IEEE-754 float32
// values -- the on-disk format for documents.embedding (a BLOB/bytea/
// LONGBLOB column on every dialect, see dialect.go) since this optimization
// replaced the previous JSON-array-of-decimal-text encoding. 4 bytes/float
// regardless of value, versus JSON's ~10-12 ASCII bytes/float, with no
// reflection-driven token scanning or strconv parsing on either side --
// embeddings are opaque numeric vectors, never inspected as JSON by a human
// or tool, so there's no readability trade-off being given up here.
//
// A nil/empty vec encodes to an empty (non-nil) byte slice, decoded back by
// DecodeEmbedding to an empty (non-nil) []float32 -- consistent with how
// json.Marshal(([]float32)(nil)) previously produced "[]" wire.
func EncodeEmbedding(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// DecodeEmbedding unpacks a []byte written by EncodeEmbedding back into a
// []float32. An empty input decodes to an empty (non-nil) slice, mirroring
// json.Unmarshal([]byte("[]"), &vec)'s previous behavior for an empty
// embedding. A length that isn't a multiple of 4 is an error -- means the
// blob wasn't written by EncodeEmbedding (or was truncated/corrupted).
func DecodeEmbedding(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("embedding blob length %d is not a multiple of 4 bytes", len(data))
	}
	vec := make([]float32, len(data)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec, nil
}
