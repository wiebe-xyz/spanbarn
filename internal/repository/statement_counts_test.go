package repository

import (
	"context"
	"testing"
)

// TestStatementCountsReplaceSpans pins that a call under WithStatementCounts
// creates no sqlite span and is counted under its span name instead.
func TestStatementCountsReplaceSpans(t *testing.T) {
	spanNames := recordSpans(t)
	repo := setupTestDB(t)
	before := len(sqliteSpanNames(spanNames()))

	ctx, calls := WithStatementCounts(context.Background())
	for range 3 {
		var n int
		if err := repo.db.QueryRowContext(ctx, "SELECT\n\tCOUNT(*) FROM spans").Scan(&n); err != nil {
			t.Fatalf("count spans: %v", err)
		}
	}
	if _, err := repo.db.ExecContext(ctx, "DELETE FROM spans WHERE id < 0"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if got := sqliteSpanNames(spanNames()); len(got) != before {
		t.Fatalf("counted calls emitted %d sqlite span(s): %v", len(got)-before, got[before:])
	}
	attrs := map[string]int64{}
	for _, kv := range calls.Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsInt64()
	}
	if attrs["db.calls.sqlite.select"] != 3 || attrs["db.calls.sqlite.delete"] != 1 {
		t.Fatalf("counts = %v, want 3 selects and 1 delete", attrs)
	}
	var sum int64
	for k, v := range attrs {
		if k != "db.calls" {
			sum += v
		}
	}
	if attrs["db.calls"] != sum {
		t.Fatalf("db.calls = %d, want the sum of the names %d (%v)", attrs["db.calls"], sum, attrs)
	}
}

func TestSqliteSpanNameSplitsOnAnyWhitespace(t *testing.T) {
	cases := map[string]string{
		"SELECT 1":               "sqlite.select",
		"SELECT\n\t(select 1)":   "sqlite.select",
		"\n  DELETE\tFROM spans": "sqlite.delete",
		"WITH t AS (SELECT 1)":   "sqlite.with",
		"":                       "sqlite.exec",
		"VACUUM":                 "sqlite.exec",
	}
	for q, want := range cases {
		if got := sqliteSpanName(context.Background(), "", q); got != want {
			t.Errorf("sqliteSpanName(%q) = %q, want %q", q, got, want)
		}
	}
}
