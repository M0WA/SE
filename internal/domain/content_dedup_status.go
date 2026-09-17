package domain

import "time"

// ContentDedupStatus is the persisted, cross-process-visible record of the
// last content-dedup batch run -- same pattern as PageRankStatus/
// EmbeddingRecomputeStatus. GroupsFound/DocumentsMerged/DurationMs describe
// the last completed run; a run in progress leaves them untouched, so a
// concurrent viewer sees the previous result, not a blank slate.
type ContentDedupStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	// GroupsFound is how many distinct groups of duplicate/near-duplicate
	// documents were found and merged into one canonical document each.
	GroupsFound int `json:"groups_found,omitempty"`
	// DocumentsMerged is the total number of non-canonical (loser)
	// documents removed across every group -- always >= GroupsFound (a
	// group of size 2 merges exactly 1 loser; a group of size 3 merges 2).
	DocumentsMerged int   `json:"documents_merged,omitempty"`
	DurationMs      int64 `json:"duration_ms,omitempty"`
}
