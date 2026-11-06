package repository

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
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

// An empty prompts shard created before the index gets it on the next startup.
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

// A prompts shard that holds rows when migration 3 runs is left without the
// index, so the writer does not read every body before it serves, and the
// group query still answers correctly there from the table.
func TestMigrateShardSkipsPromptGroupIndexOnRows(t *testing.T) {
	ctx := context.Background()
	h, err := NewDB(filepath.Join(t.TempDir(), "prompts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	p, err := goose.NewProvider(goose.DialectSQLite3, h.DB, nil,
		goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(shardVersionTable),
		goose.WithGoMigrations(shardMigrations(FamilyPrompts)...),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		project      int64
		name, status string
		weight       any
		dur, in, out int64
		cost         float64
	}{
		{1, "chat", "ok", 4, 1000, 10, 20, 0.5},
		{1, "chat", "error", nil, 3000, 5, 0, 0.25},
		{2, "embed", "ok", 1, 500, 7, 0, 0.125},
	} {
		if _, err := h.DB.ExecContext(ctx, `INSERT INTO prompt_records
			(project_id, trace_id, span_id, service, name, model, status, sample_weight, duration_us,
			 input_tokens, output_tokens, cost_usd, prompt_body, start_time_us, ingested_at)
			VALUES (?, 't', 's', 'api', ?, 'm', ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
			r.project, r.name, r.status, r.weight, r.dur, r.in, r.out, r.cost, strings.Repeat("x", 9000), at); err != nil {
			t.Fatal(err)
		}
	}

	if err := MigrateShard(ctx, h.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_prompt_records_groups'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("index count %d on a shard with rows, err %v", n, err)
	}

	for _, tc := range []struct {
		filter PromptFilter
		want   map[string]PromptGroup
	}{
		{PromptFilter{}, map[string]PromptGroup{
			"chat":  {Count: 2, WeightedCount: 5, ErrorCount: 1, TotalTimeUs: 4000, InputTokens: 15, OutputTokens: 20, CostUSD: 0.75},
			"embed": {Count: 1, WeightedCount: 1, TotalTimeUs: 500, InputTokens: 7, CostUSD: 0.125},
		}},
		{PromptFilter{ProjectID: 2}, map[string]PromptGroup{
			"embed": {Count: 1, WeightedCount: 1, TotalTimeUs: 500, InputTokens: 7, CostUSD: 0.125},
		}},
	} {
		f := tc.filter
		f.From, f.To = at.Add(-time.Hour), at.Add(time.Hour)
		where, args := f.where()
		g := promptGrouper{byKey: map[promptGroupKey]*PromptGroup{}}
		if err := g.read(ctx, h.DB, promptGroupsQuery("prompt_records", where), args); err != nil {
			t.Fatal(err)
		}
		if len(g.order) != len(tc.want) {
			t.Fatalf("project %d: %d groups, want %d", f.ProjectID, len(g.order), len(tc.want))
		}
		for _, k := range g.order {
			got, want := *g.byKey[k], tc.want[k.name]
			if got.Count != want.Count || got.WeightedCount != want.WeightedCount || got.ErrorCount != want.ErrorCount ||
				got.TotalTimeUs != want.TotalTimeUs || got.InputTokens != want.InputTokens ||
				got.OutputTokens != want.OutputTokens || got.CostUSD != want.CostUSD {
				t.Errorf("project %d, %s: got %+v, want %+v", f.ProjectID, k.name, got, want)
			}
		}
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
