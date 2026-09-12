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
