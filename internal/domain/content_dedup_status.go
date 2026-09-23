package domain

import "time"

// ContentDedupStatus is the persisted record of the last content-dedup
// batch run, same pattern as PageRankStatus/EmbeddingRecomputeStatus. A run
// in progress leaves the last-completed fields untouched.
type ContentDedupStatus struct {
	InProgress bool      `json:"in_progress"`
	LastRunAt  time.Time `json:"last_run_at,omitempty"`
	// GroupsFound is how many duplicate groups were merged into one
	// canonical document each.
	GroupsFound int `json:"groups_found,omitempty"`
	// DocumentsMerged is the total losers removed across every group --
	// always >= GroupsFound.
	DocumentsMerged int   `json:"documents_merged,omitempty"`
	DurationMs      int64 `json:"duration_ms,omitempty"`
}
