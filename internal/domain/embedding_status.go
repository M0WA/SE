package domain

import "time"

// EmbeddingRecomputeStatus is the persisted record of an embeddings
// recompute run -- live while it's in progress, and the last-completed
// summary once it finishes (mirrors domain.PageRankStatus).
type EmbeddingRecomputeStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	// Documents/Failed update live as a run progresses -- checkpointed
	// after every batch (see EmbeddingRecomputeBatchSize), not only once
	// the whole run finishes -- so a concurrently-polling admin sees real,
	// moving counts during a run that can take hours, not just 0 the
	// entire time.
	Documents int `json:"documents,omitempty"`
	// Failed is how many documents errored (Embed or write-back) -- logged
	// and skipped rather than aborting the whole run.
	Failed     int   `json:"failed,omitempty"`
	DurationMs int64 `json:"duration_ms,omitempty"`
	// LastDocID is the highest document ID (in AllDocumentIDs' own
	// ORDER BY id) a still-in-progress run has fully finished writing --
	// the checkpoint RunEmbeddingRecomputeJob resumes from if this same
	// run gets interrupted (e.g. an admin-server restart) rather than
	// starting the whole corpus over from scratch. Only meaningful while
	// InProgress is true: a completed run clears it, and a fresh, explicit
	// POST /admin/api/embeddings/recompute trigger always starts at the
	// beginning regardless of any old value left here -- resuming is only
	// ever for recovering the SAME interrupted run, never for reusing a
	// checkpoint from a run that predates a settings/model change.
	LastDocID string `json:"last_doc_id,omitempty"`
}
