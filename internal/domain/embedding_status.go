package domain

import "time"

// EmbeddingRecomputeStatus is the persisted record of the last embeddings
// recompute (mirrors domain.PageRankStatus). An in-progress run leaves the
// last-completed fields untouched.
type EmbeddingRecomputeStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	// Documents is how many documents got a freshly computed embedding.
	Documents int `json:"documents,omitempty"`
	// Failed is how many documents errored (Embed or write-back) -- logged
	// and skipped rather than aborting the whole run.
	Failed     int   `json:"failed,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
}
