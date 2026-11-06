package repository

import (
	"context"
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

// addPromptGroupIndex creates the covering index for the prompt group query.
// It runs after addPromptSampleWeight, since the index names that column. A
// handle whose main file has no prompt_records is left alone. Building it reads
// every row once, bodies included, so its cost scales with the file's size.
func addPromptGroupIndex(ctx context.Context, db schemaExec) error {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM main.sqlite_master WHERE type = 'table' AND name = 'prompt_records'`).Scan(&tables); err != nil {
		return fmt.Errorf("prompt group index: %w", err)
	}
	if tables == 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, promptGroupIndexDDL); err != nil {
		return fmt.Errorf("prompt group index: %w", err)
	}
	return nil
}
