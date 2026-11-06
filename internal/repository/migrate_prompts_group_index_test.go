package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The prompts list's group query reads index pages only, with or without a
// service or model filter, on a single file and on a prompts shard (#457).
func TestPromptGroupsQueryUsesCoveringIndex(t *testing.T) {
	ctx := context.Background()
	single, err := NewDB(filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	if err := MigrateSingleFile(ctx, single.DB); err != nil {
		t.Fatal(err)
	}
	shard, err := NewDB(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer shard.Close()
	if err := MigrateShard(ctx, shard.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}

	to := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	filters := map[string]PromptFilter{
		"project":         {ProjectID: 1},
		"project+service": {ProjectID: 1, Service: "api"},
		"project+model":   {ProjectID: 1, Model: "m"},
		"all projects":    {},
	}
	for _, db := range []struct {
		name string
		db   *sql.DB
	}{{"single file", single.DB}, {"prompts shard", shard.DB}} {
		for name, f := range filters {
			f.From, f.To = to.Add(-24*time.Hour), to
			where, args := f.where()
			plan := dbQueryPlan(t, db.db, promptGroupsQuery("prompt_records", where), args)
			if !strings.Contains(plan, "COVERING INDEX idx_prompt_records_groups") {
				t.Errorf("%s, %s: plan does not cover the query from the group index:\n%s", db.name, name, plan)
			}
		}
	}
}

// A prompts shard created before the index gets it on the next startup.
func TestMigrateShardAddsPromptGroupIndex(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := MigrateShard(ctx, h.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `DROP INDEX idx_prompt_records_groups`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB.ExecContext(ctx, `DELETE FROM `+shardVersionTable+` WHERE version_id = 3`); err != nil {
		t.Fatal(err)
	}
	if err := MigrateShard(ctx, h.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_prompt_records_groups'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index count %d, err %v", n, err)
	}
}

func dbQueryPlan(t *testing.T, db *sql.DB, query string, args []any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		b.WriteString(detail + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
