package sqlrepo

import (
	"errors"
	"testing"
)

// TestIsAlreadyExistsError guards against a real production incident:
// search/admin/crawl all call New() (and so ensureHostIndex/
// ensureCrawledAtIndex, and every ADD COLUMN migration) concurrently on
// startup against the same Postgres database. Neither CREATE INDEX IF NOT
// EXISTS nor a plain ADD COLUMN is atomic across concurrent sessions there
// -- the losing session gets a unique-violation on the system catalog (for
// an index) or an "already exists" error (for a column) instead of a clean
// no-op -- which previously surfaced as a hard startup failure (and a
// process crash-loop) even though the index/column was, in fact,
// successfully created by the winner. The crawl_jobs/scheduled_crawls
// column migrations added for per-crawl overrides hit exactly this the
// first time all three processes restarted at once against an existing
// database (see the ADD COLUMN case below).
func TestIsAlreadyExistsError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unrelated error", errors.New("connection refused"), false},
		{"plain already exists", errors.New(`relation "idx_documents_host" already exists`), true},
		{
			"postgres catalog race",
			errors.New(`creating crawled_at index: ERROR: duplicate key value violates unique constraint "pg_class_relname_nsp_index" (SQLSTATE 23505)`),
			true,
		},
		{
			"postgres concurrent ADD COLUMN race",
			errors.New(`adding fetch_timeout_seconds column: ERROR: column "fetch_timeout_seconds" of relation "scheduled_crawls" already exists (SQLSTATE 42701)`),
			true,
		},
		{
			"mysql concurrent ADD COLUMN race",
			errors.New(`Error 1060 (42S21): Duplicate column name 'fetch_timeout_seconds'`),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAlreadyExistsError(tc.err); got != tc.want {
				t.Errorf("isAlreadyExistsError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsMissingExtensionError guards against the exact production incident
// EnableANN is designed around: "CREATE EXTENSION vector" failing because
// pgvector isn't installed at the OS/server level at all (distinct from a
// permission problem, which is a plain "permission denied" error and
// should be classified/logged differently) must be recognized so EnableANN
// can log a specific, actionable warning -- and, either way, never crash
// the process (see TestEnableANN_NonFatalWhenCreateExtensionFails in
// ann_test.go for that half of the contract).
func TestIsMissingExtensionError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", errors.New("connection refused"), false},
		{"permission denied is a distinct class, not this one", errors.New("permission denied to create extension \"vector\""), false},
		{
			"missing extension control file",
			errors.New(`pq: could not open extension control file "/usr/share/postgresql/16/extension/vector.control": No such file or directory`),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingExtensionError(tc.err); got != tc.want {
				t.Errorf("isMissingExtensionError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
