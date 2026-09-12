package sqlrepo

import (
	"errors"
	"testing"
)

// TestIsIndexAlreadyExistsError guards against a real production incident:
// search/admin/crawl all call New() (and so ensureHostIndex/
// ensureCrawledAtIndex) concurrently on startup against the same Postgres
// database. CREATE INDEX IF NOT EXISTS is not atomic across concurrent
// sessions there -- the losing session gets a unique-violation on the
// system catalog instead of a clean no-op -- which previously surfaced as
// a hard startup failure (and a process crash-loop) even though the index
// was, in fact, successfully created by the winner.
func TestIsIndexAlreadyExistsError(t *testing.T) {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isIndexAlreadyExistsError(tc.err); got != tc.want {
				t.Errorf("isIndexAlreadyExistsError(%v) = %v, want %v", tc.err, got, tc.want)
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
