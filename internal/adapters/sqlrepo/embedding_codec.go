package sqlrepo

import (
	"encoding/binary"
	"fmt"
	"math"
)

// EncodeEmbedding packs vec as little-endian IEEE-754 float32s -- the
// on-disk format for documents.embedding, replacing a previous
// JSON-array encoding (4 bytes/float vs JSON's ~10-12, no parsing
// overhead; embeddings are never human-inspected, so no readability is
// lost). A nil/empty vec encodes to an empty (non-nil) byte slice.
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
