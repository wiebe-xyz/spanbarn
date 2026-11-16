package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

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
// index, so the writer does not read every body before it serves.
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
	if _, err := h.DB.ExecContext(ctx, `INSERT INTO prompt_records
		(project_id, trace_id, span_id, service, name, model, status, duration_us, prompt_body, start_time_us, ingested_at)
		VALUES (1, 't', 's', 'api', 'chat', 'm', 'ok', 1000, 'body', 1, ?)`, time.Now()); err != nil {
		t.Fatal(err)
	}

	if err := MigrateShard(ctx, h.DB, FamilyPrompts); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_prompt_records_groups'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("index count %d on a shard with rows, err %v", n, err)
	}

}
