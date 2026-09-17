package domain

import "time"

// EmbeddingRecomputeStatus is the persisted, cross-process-visible record
// of the last embeddings recompute, written to a shared SettingsStore key
// (mirrors domain.PageRankStatus). LastRunAt/Documents/Failed/DurationMs
// describe the last completed run; an in-progress run leaves them
// untouched, so a viewer sees the previous result, not a blank slate.
type EmbeddingRecomputeStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	// Documents is how many documents got a freshly computed embedding.
	Documents int `json:"documents,omitempty"`
	// Failed is how many documents' Embed call (or the write-back) errored
	// -- logged individually and skipped rather than aborting the whole
	// run, so a flaky embeddings endpoint or a single rate-limited request
	// doesn't lose progress on an otherwise-large corpus.
	Failed     int   `json:"failed,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
}
