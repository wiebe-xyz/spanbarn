package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (r *Repository) InsertPromptRecords(records []PromptRecord) error {
	if len(records) == 0 {
		return nil
	}
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	return r.execIngest(FamilyPrompts, func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO prompt_records
			(project_id, trace_id, span_id, parent_span_id, service, name,
			 gen_ai_system, model, temperature, max_tokens,
			 prompt_body, response_body,
			 input_tokens, output_tokens, total_tokens,
			 cached_input_tokens, reasoning_output_tokens,
			 cost_usd, input_cost_usd, output_cost_usd, duration_us,
			 status, finish_reason,
			 prompt_template, prompt_hash, outcome, quality_score,
			 feature_flag_key, feature_flag_variant, start_time_us)
			VALUES (?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?,
			        ?, ?,
			        ?, ?, ?,
			        ?, ?,
			        ?, ?, ?, ?,
			        ?, ?,
			        ?, ?, ?, ?,
			        ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, rec := range records {
			var parentID *string
			if rec.ParentSpanID != "" {
				parentID = &rec.ParentSpanID
			}
			if _, err := stmt.ExecContext(ctx,
				rec.ProjectID, rec.TraceID, rec.SpanID, parentID, rec.Service, rec.Name,
				rec.GenAISystem, rec.Model, rec.Temperature, rec.MaxTokens,
				rec.PromptBody, rec.ResponseBody,
				rec.InputTokens, rec.OutputTokens, rec.TotalTokens,
				rec.CachedInputTokens, rec.ReasoningOutputTokens,
				rec.CostUSD, rec.InputCostUSD, rec.OutputCostUSD, rec.DurationUs,
				rec.Status, rec.FinishReason,
				rec.PromptTemplate, rec.PromptHash, rec.Outcome, rec.QualityScore,
				rec.FeatureFlagKey, rec.FeatureFlagVariant, rec.StartTimeUs,
			); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func (r *Repository) QueryPromptRecords(f PromptFilter) ([]PromptRecord, error) {
	where, args := f.where()

	query := func(table string) string {
		q := promptRecordColumns + ` FROM ` + table
		if len(where) > 0 {
			q += " WHERE " + strings.Join(where, " AND ")
		}
		return q + " ORDER BY ingested_at DESC"
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if p := r.shardReaders.pool(FamilyPrompts); p != nil {
		return r.promptRecordsBySegment(p, query, args, limit, f.Offset)
	}
	q := query("prompt_records") + fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}
	return r.scanPromptRecords(r.db, q, args...)
}

// where builds the WHERE conditions shared by the prompt list and the prompt
// groups.
func (f PromptFilter) where() ([]string, []any) {
	b := &whereBuilder{}
	if f.ProjectID != 0 {
		b.add("project_id = ?", f.ProjectID)
	}
	for _, c := range []struct{ col, val string }{
		{"service", f.Service}, {"name", f.Name}, {"model", f.Model}, {"gen_ai_system", f.GenAISystem},
		{"status", f.Status}, {"finish_reason", f.FinishReason}, {"prompt_hash", f.PromptHash},
	} {
		if c.val != "" {
			b.add(c.col+" = ?", c.val)
		}
	}
	if f.MinDuration > 0 {
		b.add("duration_us >= ?", f.MinDuration)
	}
	b.addTimeRange("ingested_at", f.From, f.To)
	return b.where, b.args
}

// promptRecordsBySegment is the prompts list over a sharded family. Through
// the family's UNION ALL view, ORDER BY ... LIMIT would sort every matching
// row of every shard, bodies included. Instead each shard, and main, returns
// its own newest offset+limit rows along its ingested_at index, and the
// segments are merged here.
func (r *Repository) promptRecordsBySegment(p *familyPool, query func(table string) string, args []any, limit, offset int) ([]PromptRecord, error) {
	need := limit + offset
	var all []PromptRecord
	for _, schema := range p.segments {
		recs, err := r.scanPromptRecords(p.db.DB, query(schema+".prompt_records")+fmt.Sprintf(" LIMIT %d", need), args...)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].IngestedAt.After(all[j].IngestedAt) })
	if offset >= len(all) {
		return nil, nil
	}
	return all[offset:min(len(all), need)], nil
}

const promptRecordColumns = `SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), service, name,
		gen_ai_system, model, temperature, max_tokens,
		prompt_body, response_body,
		input_tokens, output_tokens, total_tokens,
		cached_input_tokens, reasoning_output_tokens,
		cost_usd, input_cost_usd, output_cost_usd, duration_us,
		status, finish_reason,
		prompt_template, prompt_hash, outcome, quality_score,
		feature_flag_key, feature_flag_variant, start_time_us, ingested_at`

func (r *Repository) GetPromptRecordsByTraceID(traceID string) ([]PromptRecord, error) {
	return r.scanPromptRecords(r.reader(FamilyPrompts),
		promptRecordColumns+` FROM prompt_records WHERE trace_id = ? ORDER BY start_time_us`,
		traceID,
	)
}

// DeletePromptRecordsOlderThanLimited removes at most max prompt records
// ingested before cutoff, reporting whether more were left for the next cycle.
// It deletes per project in bounded batches so each batch seeks
// idx_prompt_records_project_ingested and the write lock is released between
// batches; the rows carry full prompt and response bodies, so a single
// unbounded DELETE over weeks of them would hold the writer for minutes.
func (r *Repository) DeletePromptRecordsOlderThanLimited(ctx context.Context, cutoff time.Time, max int64) (int64, bool, error) {
	return r.deleteOlderThanPerProjectLimited(ctx, "prompt_records", "ingested_at", cutoff, max)
}

func (r *Repository) scanPromptRecords(db *sql.DB, query string, args ...any) ([]PromptRecord, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PromptRecord
	for rows.Next() {
		var rec PromptRecord
		if err := rows.Scan(
			&rec.ID, &rec.ProjectID, &rec.TraceID, &rec.SpanID, &rec.ParentSpanID,
			&rec.Service, &rec.Name,
			&rec.GenAISystem, &rec.Model, &rec.Temperature, &rec.MaxTokens,
			&rec.PromptBody, &rec.ResponseBody,
			&rec.InputTokens, &rec.OutputTokens, &rec.TotalTokens,
			&rec.CachedInputTokens, &rec.ReasoningOutputTokens,
			&rec.CostUSD, &rec.InputCostUSD, &rec.OutputCostUSD, &rec.DurationUs,
			&rec.Status, &rec.FinishReason,
			&rec.PromptTemplate, &rec.PromptHash, &rec.Outcome, &rec.QualityScore,
			&rec.FeatureFlagKey, &rec.FeatureFlagVariant, &rec.StartTimeUs, &rec.IngestedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
