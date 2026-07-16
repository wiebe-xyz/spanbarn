package repository

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) InsertPromptRecords(records []PromptRecord) error {
	if len(records) == 0 {
		return nil
	}
	// Writing telemetry must not emit telemetry. See WithoutSpanTracing.
	ctx := WithoutSpanTracing(context.Background())
	return r.execLow(func() error {
		tx, err := r.db.BeginTx(ctx, nil)
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
	var where []string
	var args []any

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Model != "" {
		where = append(where, "model = ?")
		args = append(args, f.Model)
	}
	if f.GenAISystem != "" {
		where = append(where, "gen_ai_system = ?")
		args = append(args, f.GenAISystem)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.FinishReason != "" {
		where = append(where, "finish_reason = ?")
		args = append(args, f.FinishReason)
	}
	if f.PromptHash != "" {
		where = append(where, "prompt_hash = ?")
		args = append(args, f.PromptHash)
	}
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}

	q := `SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), service, name,
		gen_ai_system, model, temperature, max_tokens,
		prompt_body, response_body,
		input_tokens, output_tokens, total_tokens,
		cached_input_tokens, reasoning_output_tokens,
		cost_usd, input_cost_usd, output_cost_usd, duration_us,
		status, finish_reason,
		prompt_template, prompt_hash, outcome, quality_score,
		feature_flag_key, feature_flag_variant, start_time_us, ingested_at
		FROM prompt_records`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ingested_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	return r.scanPromptRecords(q, args...)
}

func (r *Repository) GetPromptRecordsByTraceID(traceID string) ([]PromptRecord, error) {
	return r.scanPromptRecords(
		`SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), service, name,
		gen_ai_system, model, temperature, max_tokens,
		prompt_body, response_body,
		input_tokens, output_tokens, total_tokens,
		cached_input_tokens, reasoning_output_tokens,
		cost_usd, input_cost_usd, output_cost_usd, duration_us,
		status, finish_reason,
		prompt_template, prompt_hash, outcome, quality_score,
		feature_flag_key, feature_flag_variant, start_time_us, ingested_at
		FROM prompt_records WHERE trace_id = ? ORDER BY start_time_us`,
		traceID,
	)
}

func (r *Repository) DeletePromptRecordsOlderThan(cutoff time.Time) (int64, error) {
	return r.execLowAffecting("DELETE FROM prompt_records WHERE ingested_at < ?", cutoff)
}

func (r *Repository) scanPromptRecords(query string, args ...any) ([]PromptRecord, error) {
	rows, err := r.db.Query(query, args...)
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
