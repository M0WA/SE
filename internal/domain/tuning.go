package domain

import "sync"

// TuningSettings holds the hybrid search's runtime-adjustable scoring
// parameters. Safe for concurrent use: read on every search request,
// written from the admin tuning panel.
type TuningSettings struct {
	mu    sync.RWMutex
	alpha float64
	k1    float64
	b     float64
	// pageRankWeight blends a document's normalized PageRank score into
	// FinalScore (see hybridSearchService.Search) -- 0 (the default) means
	// zero influence, exactly reproducing ranking as it was before
	// PageRank existed. Kept as its own field (with its own accessors)
	// rather than a fourth positional Get/Set argument, so every existing
	// caller of the alpha/k1/b triple is untouched by this addition.
	pageRankWeight float64
}

func NewTuningSettings(alpha, k1, b float64) *TuningSettings {
	return &TuningSettings{alpha: alpha, k1: k1, b: b}
}

// Get returns the current alpha (BM25 vs. semantic blend weight, 0-1), k1
// (term-frequency saturation) and b (length normalization) values.
func (s *TuningSettings) Get() (alpha, k1, b float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alpha, s.k1, s.b
}

// Set updates the tuning parameters. Values are clamped to sane ranges
// rather than rejected, since this is an admin convenience knob, not a
// user-facing form that needs field-level validation errors.
func (s *TuningSettings) Set(alpha, k1, b float64) {
	alpha = clamp(alpha, 0, 1)
	if k1 < 0 {
		k1 = 0
	}
	b = clamp(b, 0, 1)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.alpha, s.k1, s.b = alpha, k1, b
}

// PageRankWeight returns the current PageRank blend weight (see
// pageRankWeight): 0 means a search's ranking has zero PageRank influence,
// 1 means FinalScore is driven entirely by normalized PageRank.
func (s *TuningSettings) PageRankWeight() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pageRankWeight
}

// SetPageRankWeight updates the blend weight, clamped to [0,1] the same
// way Set clamps alpha/b -- an admin convenience knob, not a validated
// form field.
func (s *TuningSettings) SetPageRankWeight(w float64) {
	w = clamp(w, 0, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pageRankWeight = w
}

// TuningValues is a JSON-serializable snapshot of TuningSettings' fields,
// for callers (the settings store, the admin API) that need to read or
// write them as a single value rather than through the individual
// Get()/Set()/PageRankWeight()/SetPageRankWeight() accessors.
type TuningValues struct {
	Alpha          float64 `json:"alpha"`
	K1             float64 `json:"k1"`
	B              float64 `json:"b"`
	PageRankWeight float64 `json:"pagerank_weight"`
}

func (s *TuningSettings) Values() TuningValues {
	alpha, k1, b := s.Get()
	return TuningValues{Alpha: alpha, K1: k1, B: b, PageRankWeight: s.PageRankWeight()}
}

func (s *TuningSettings) SetValues(v TuningValues) {
	s.Set(v.Alpha, v.K1, v.B)
	s.SetPageRankWeight(v.PageRankWeight)
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
