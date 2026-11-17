package repository

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// mergeSteps is every step a merge runs, in order: the core family, then the
// spans family. The spans and the traces move first and the tiers after them,
// so a read of the target meets the tiers last.
var mergeSteps = append(append([]mergeStep{}, coreMergeSteps...), spansMergeSteps...)

var spansMergeSteps = []mergeStep{
	{family: "spans", fam: engine.FamilySpans, table: "spans", batch: moveByID("spans", moveOpts{stamp: true})},
	{family: "spans", fam: engine.FamilySpans, table: "error_samples", batch: moveByID("error_samples", moveOpts{stamp: true})},
	{family: "spans", fam: engine.FamilySpans, table: "trace_summaries", batch: keyedMove(traceSummariesMerge)},
	{family: "spans", fam: engine.FamilySpans, table: "error_exemplars", batch: keyedMove(errorExemplarsMerge)},
	{family: "spans", fam: engine.FamilySpans, table: "aggregates", batch: keyedMove(aggregatesMerge)},
	{family: "spans", fam: engine.FamilySpans, table: "span_rollups", batch: keyedMove(spanRollupsMerge)},
	{family: "spans", fam: engine.FamilySpans, table: "span_edges", batch: keyedMove(spanEdgesMerge)},
	{family: "spans", fam: engine.FamilySpans, table: "spans_staging", batch: moveByID("spans_staging", moveOpts{stamp: true})},
}

// keyedTable describes a table with a unique key over project_id, whose rows
// can collide when two projects meet. A row moves by an INSERT ... SELECT with
// the target project and environment, and a row that finds its key taken is
// folded into the row there by the SET list the writers use.
type keyedTable struct {
	table string
	// cols is every column a row copies. The SELECT list stamps project_id and
	// environment and passes the rest through.
	cols []string
	// key is the unique key, project_id first, in an order that is unique per
	// row, so a batch is repeatable.
	key []string
	// onConflict is the SET list of the DO UPDATE clause; empty keeps the target row.
	onConflict string
}

func splitColumns(list string) []string {
	parts := strings.Split(list, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

var (
	traceSummaryMergeColumns = []string{"project_id", "trace_id", "root_name", "root_service", "root_duration_us", "start_time_us",
		"span_count", "has_error", "ingested_at", "expires_at", "has_root", "orphan_count", "durable"}

	// A trace seen from two projects keeps one summary: the span counts add, the
	// flags and the durable mark take the larger value, the window widens, and the
	// root comes from whichever side saw it.
	traceSummariesMerge = keyedTable{
		table: "trace_summaries", cols: traceSummaryMergeColumns, key: []string{"project_id", "trace_id"},
		onConflict: `span_count = span_count + excluded.span_count,
			orphan_count = orphan_count + excluded.orphan_count,
			has_error = MAX(has_error, excluded.has_error),
			durable = MAX(durable, excluded.durable),
			start_time_us = MIN(start_time_us, excluded.start_time_us),
			ingested_at = MIN(ingested_at, excluded.ingested_at),
			expires_at = CASE WHEN expires_at IS NULL OR excluded.expires_at IS NULL THEN NULL ELSE MAX(expires_at, excluded.expires_at) END,
			root_name = CASE WHEN has_root = 0 AND excluded.has_root = 1 THEN excluded.root_name ELSE root_name END,
			root_service = CASE WHEN has_root = 0 AND excluded.has_root = 1 THEN excluded.root_service ELSE root_service END,
			root_duration_us = CASE WHEN has_root = 0 AND excluded.has_root = 1 THEN excluded.root_duration_us ELSE root_duration_us END,
			has_root = MAX(has_root, excluded.has_root)`,
	}

	// An exemplar is a pointer to one trace per operation and day; the target keeps its own.
	errorExemplarsMerge = keyedTable{
		table: "error_exemplars", cols: []string{"project_id", "service", "name", "day", "trace_id"},
		key: []string{"project_id", "service", "name", "day"},
	}

	aggregatesMerge = keyedTable{
		table: "aggregates",
		cols: []string{"project_id", "environment", "service", "operation", "resource", "kind", "bucket", "count", "error_count",
			"p50_us", "p95_us", "p99_us", "max_us", "sum_duration_us", "weighted_count", "weighted_spans"},
		key: splitColumns(aggregateKey), onConflict: aggregateMergeSQL,
	}

	spanRollupsMerge = keyedTable{
		table: "span_rollups", cols: splitColumns(spanRollupColumns), key: splitColumns(spanRollupKey), onConflict: spanRollupMergeSQL,
	}

	spanEdgesMerge = keyedTable{
		table: "span_edges", cols: splitColumns(spanEdgeColumns), key: splitColumns(spanEdgeKey), onConflict: spanEdgeMergeSQL,
	}
)

// stamped returns the expression and argument a column takes on the target side
// of a move: the target project, the merge environment where the row has none.
func (k keyedTable) stamped(spec MergeSpec, col string) (string, []any) {
	switch col {
	case "project_id":
		return "?", []any{spec.TargetID}
	case "environment":
		return "COALESCE(NULLIF(environment, ''), ?)", []any{spec.Environment}
	}
	return col, nil
}

// selectExprs is the SELECT list of the copy and the arguments its markers take.
func (k keyedTable) selectExprs(spec MergeSpec) (string, []any) {
	exprs := make([]string, len(k.cols))
	var args []any
	for i, c := range k.cols {
		var a []any
		exprs[i], a = k.stamped(spec, c)
		args = append(args, a...)
	}
	return strings.Join(exprs, ", "), args
}

// collisionSQL counts the rows of one batch whose target key is already taken.
// It reads the batch as the move will, then looks each key up in the target.
func (k keyedTable) collisionSQL(spec MergeSpec) (string, []any) {
	keyList := strings.Join(k.key, ", ")
	var preds []string
	var args []any
	for _, c := range k.key {
		switch c {
		case "project_id":
			preds = append(preds, "x.project_id = ?")
			args = append(args, spec.TargetID)
		case "environment":
			preds = append(preds, "x.environment = COALESCE(NULLIF(s.environment, ''), ?)")
			args = append(args, spec.Environment)
		default:
			preds = append(preds, fmt.Sprintf("x.%[1]s = s.%[1]s", c))
		}
	}
	q := fmt.Sprintf(`SELECT count(*) FROM (SELECT %s FROM %s WHERE project_id = ? ORDER BY %s LIMIT ?) AS s
		WHERE EXISTS (SELECT 1 FROM %s AS x WHERE %s)`, keyList, k.table, keyList, k.table, strings.Join(preds, " AND "))
	return q, args
}

// keyedMove moves up to n rows of a keyed table.
func keyedMove(k keyedTable) func(*sql.Tx, MergeSpec, int) (MergeStepResult, error) {
	keyList := strings.Join(k.key, ", ")
	conflict := "DO NOTHING"
	if k.onConflict != "" {
		conflict = "DO UPDATE SET " + k.onConflict
	}
	return func(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error) {
		var res MergeStepResult
		countSQL, lookupArgs := k.collisionSQL(spec)
		if err := tx.QueryRow(countSQL, append([]any{spec.SourceID, n}, lookupArgs...)...).Scan(&res.Collided); err != nil {
			return res, err
		}
		exprs, sel := k.selectExprs(spec)
		insert := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE project_id = ? ORDER BY %s LIMIT ? ON CONFLICT (%s) %s",
			k.table, strings.Join(k.cols, ", "), exprs, k.table, keyList, keyList, conflict)
		if _, err := tx.Exec(insert, append(sel, spec.SourceID, n)...); err != nil {
			return res, err
		}
		pick := fmt.Sprintf("SELECT %s FROM %s WHERE project_id = ? ORDER BY %s LIMIT ?", keyList, k.table, keyList)
		del, err := tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE project_id = ? AND (%s) IN (%s)", k.table, keyList, pick),
			spec.SourceID, spec.SourceID, n)
		if err != nil {
			return res, err
		}
		res.Moved, _ = del.RowsAffected()
		return res, nil
	}
}
