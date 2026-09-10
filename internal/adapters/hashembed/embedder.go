package hashembed

import (
	"context"
	"hash/fnv"
	"math"

	"searchengine/internal/domain"
)

// Embedder erzeugt einen deterministischen Pseudo-Embedding-Vektor via
// Feature Hashing. Kein echtes Sprachverständnis, aber abhängigkeitsfrei.
type Embedder struct {
	dims int
}

func New(dims int) *Embedder {
	if dims <= 0 {
		dims = 128
	}
	return &Embedder{dims: dims}
}

func (e *Embedder) Dimensions() int { return e.dims }

func (e *Embedder) Embed(_ context.Context, text string) ([]float32, error) {
	vec := make([]float32, e.dims)
	tokens := domain.Tokenize(text)
	for _, tok := range tokens {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		idx := int(h.Sum32()) % e.dims
		if idx < 0 {
			idx += e.dims
		}
		sign := float32(1)
		if h.Sum32()%2 == 0 {
			sign = -1
		}
		vec[idx] += sign
	}

	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return vec, nil
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec, nil
}
