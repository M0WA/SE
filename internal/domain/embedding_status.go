package domain

import "time"

// EmbeddingRecomputeStatus is the persisted, cross-process-visible record
// of the last embeddings recompute (see application.RunEmbeddingRecomputeJobWithStatus)
// -- written to a shared SettingsStore key so the admin Settings page
// shows whether a recompute triggered by any admin-server instance is
// currently running, and what the last completed run found, the same way
// domain.PageRankStatus already does for PageRank recomputes.
// LastRunAt/Documents/Failed/DurationMs describe the last run that
// actually completed; a run currently in progress doesn't touch them
// until it finishes, so a concurrent viewer still sees the previous
// result rather than a blank slate while InProgress is true.
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
