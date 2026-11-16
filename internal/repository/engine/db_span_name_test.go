package engine

import (
	"context"
	"testing"
)

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
