package domain

import "time"

// ContentDedupStatus is the persisted, cross-process-visible record of the
// last content-dedup batch run (see application.RunContentDedupJobWithStatus)
// -- written to a shared SettingsStore key so the admin content-dedup page
// shows whether a run triggered by any process (the periodic ticker, a
// post-crawl trigger, or an admin's "recompute now" click) is currently
// running, and what the last completed run found, the same way
// domain.PageRankStatus/EmbeddingRecomputeStatus already do for their own
// batch jobs. GroupsFound/DocumentsMerged/DurationMs describe the last run
// that actually completed; a run currently in progress doesn't touch them
// until it finishes, so a concurrent viewer still sees the previous result
// rather than a blank slate while InProgress is true.
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
