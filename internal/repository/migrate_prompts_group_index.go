package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// promptGroupIndexDDL covers every column QueryPromptGroups reads (#457).
// Without it each matching row is read from the table, and the columns the
// group query needs sit after prompt_body and response_body: SQLite walks a
// body's overflow pages to reach a later column, so the 24h prompts list read
// every body byte in its window. With it the query reads index pages only.
// It starts with ingested_at: the prompts page asks for every project, so the
// window is the one range every group query has.
const promptGroupIndexDDL = `CREATE INDEX IF NOT EXISTS main.idx_prompt_records_groups ON prompt_records(
	ingested_at, project_id, name, model, service, gen_ai_system, status,
	duration_us, sample_weight, input_tokens, output_tokens, cost_usd)`

// addPromptGroupIndex creates the covering index for the prompt group query
// on an empty prompt_records only. It runs after addPromptSampleWeight, since
// the index names that column. Building it on a table that holds rows reads
// every row once, bodies included, and the writer would do that for every
// prompts shard before it serves. So a new shard gets the index when it is
// created, and so does main once retention has emptied it. A shard that
// already holds rows is left without it: the group query still answers there
// from the table, as before, and the shard ages out under prompt retention.
// A handle whose main file has no prompt_records is left alone.
func addPromptGroupIndex(ctx context.Context, db schemaExec) error {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'prompt_records'`).Scan(&tables); err != nil {
		return fmt.Errorf("prompt group index: %w", err)
	}
	if tables == 0 {
		return nil
	}
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM main.prompt_records LIMIT 1`).Scan(&one)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("prompt group index: %w", err)
	}
	if _, err := db.ExecContext(ctx, promptGroupIndexDDL); err != nil {
		return fmt.Errorf("prompt group index: %w", err)
	}
	return nil
}
