package sqlrepo

import (
	"database/sql"
	"testing"
)

func TestNullableTimeString_NilReturnsInvalid(t *testing.T) {
	got := nullableTimeString(nil)
	if got.Valid {
		t.Errorf("expected an invalid (NULL) result for a nil *time.Time, got %+v", got)
	}
}

func TestNullableTimeString_NonNilReturnsFormattedValid(t *testing.T) {
	tm := parseCrawledAt("2026-01-01T00:00:00Z")
	got := nullableTimeString(&tm)
	if !got.Valid || got.String != "2026-01-01T00:00:00Z" {
		t.Errorf("expected a valid formatted timestamp, got %+v", got)
	}
}

func TestParseCrawledAt_InvalidStringReturnsZeroValue(t *testing.T) {
	got := parseCrawledAt("not-a-timestamp")
	if !got.IsZero() {
		t.Errorf("expected the zero time for an unparseable string, got %v", got)
	}
}

func TestParseNullableCrawledAt_InvalidNullStringReturnsNil(t *testing.T) {
	if got := parseNullableCrawledAt(sql.NullString{Valid: false}); got != nil {
		t.Errorf("expected nil for an invalid (NULL) sql.NullString, got %v", got)
	}
}

func TestParseNullableCrawledAt_UnparseableStringReturnsNil(t *testing.T) {
	if got := parseNullableCrawledAt(sql.NullString{Valid: true, String: "not-a-timestamp"}); got != nil {
		t.Errorf("expected nil for a valid-but-unparseable stored value, got %v", got)
	}
}

func TestParseNullableCrawledAt_ValidStringReturnsPointer(t *testing.T) {
	got := parseNullableCrawledAt(sql.NullString{Valid: true, String: "2026-01-01T00:00:00Z"})
	if got == nil {
		t.Fatal("expected a non-nil *time.Time")
	}
	if got.Format(crawledAtLayout) != "2026-01-01T00:00:00Z" {
		t.Errorf("expected the parsed timestamp preserved, got %v", got)
	}
}
