package repository

import (
	"database/sql"
	"fmt"
)

// coreMergeSteps are the tables of the core family a merge moves, in order. The
// ingest keys come first so new records already arrive under the target while
// the rest moves. slo_counts has no step: it keys on the SLO id and follows its
// SLO (FR-014). Boards keep their panels, which key on the board id.
var coreMergeSteps = []mergeStep{
	{family: "core", table: "api_keys", batch: moveByID("api_keys", true, nil)},
	{family: "core", table: "alerts", batch: moveByID("alerts", false, nil)},
	{family: "core", table: "boards", batch: moveByID("boards", false, nil)},
	{family: "core", table: "saved_queries", batch: moveByID("saved_queries", false, nil)},
	{family: "core", table: "releases", batch: moveByID("releases", false, nil)},
	{family: "core", table: "slos", batch: moveByID("slos", false, renameClashing("slos", "name"))},
	{family: "core", table: "calculated_fields", batch: moveByID("calculated_fields", false, renameClashing("calculated_fields", "name"))},
	{family: "core", table: "trace_exclusions", batch: moveByID("trace_exclusions", false, dropClashing("trace_exclusions", "operation"))},
	{family: "core", table: "pinned_traces", batch: moveByID("pinned_traces", false, dropClashing("pinned_traces", "trace_id"))},
	{family: "core", table: "span_ledger", batch: moveLedger},
}

// clashFunc resolves the rows of the source whose key the target already holds,
// before a batch moves, and returns how many it resolved.
type clashFunc func(tx *sql.Tx, spec MergeSpec) (int64, error)

// moveByID moves up to n rows of a table that has an integer id and a
// project_id. With withEnv the row's environment becomes the merge environment
// when it has none of its own. clash, when set, resolves unique-name collisions
// first.
func moveByID(table string, withEnv bool, clash clashFunc) func(*sql.Tx, MergeSpec, int) (MergeStepResult, error) {
	return func(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error) {
		var res MergeStepResult
		if clash != nil {
			c, err := clash(tx, spec)
			if err != nil {
				return res, err
			}
			res.Collided = c
		}
		pick := fmt.Sprintf("SELECT id FROM %s WHERE project_id = ? ORDER BY id LIMIT ?", table)
		var last sql.NullInt64
		if err := tx.QueryRow("SELECT max(id) FROM ("+pick+")", spec.SourceID, n).Scan(&last); err != nil {
			return res, err
		}
		set := "project_id = ?"
		args := []any{spec.TargetID}
		if withEnv {
			set += ", environment = COALESCE(NULLIF(environment, ''), ?)"
			args = append(args, spec.Environment)
		}
		args = append(args, spec.SourceID, n)
		r, err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s WHERE id IN (%s)", table, set, pick), args...)
		if err != nil {
			return res, err
		}
		res.Moved, _ = r.RowsAffected()
		res.LastMoved = last.Int64
		return res, nil
	}
}

// renameClashing gives a source row whose name the target uses the name with the
// source slug appended, so both settings survive and the unique key holds.
func renameClashing(table, col string) clashFunc {
	return func(tx *sql.Tx, spec MergeSpec) (int64, error) {
		r, err := tx.Exec(fmt.Sprintf(
			"UPDATE %[1]s SET %[2]s = %[2]s || ' (' || ? || ')' WHERE project_id = ? AND %[2]s IN (SELECT %[2]s FROM %[1]s WHERE project_id = ?)",
			table, col), spec.SourceSlug, spec.SourceID, spec.TargetID)
		if err != nil {
			return 0, err
		}
		return r.RowsAffected()
	}
}

// dropClashing removes a source row the target already holds an equal of, a
// trace exclusion for the same operation or a pin of the same trace. Both are
// configuration, and the target keeps the equal row.
func dropClashing(table, col string) clashFunc {
	return func(tx *sql.Tx, spec MergeSpec) (int64, error) {
		r, err := tx.Exec(fmt.Sprintf(
			"DELETE FROM %[1]s WHERE project_id = ? AND %[2]s IN (SELECT %[2]s FROM %[1]s WHERE project_id = ?)",
			table, col), spec.SourceID, spec.TargetID)
		if err != nil {
			return 0, err
		}
		return r.RowsAffected()
	}
}

type ledgerRow struct {
	bucket int64
	env    string
	counts [7]int64
}

const ledgerColumns = "accepted, sampled_out, shed_unsampled, lost_in_buffer, forwarded, processed, failed"

// moveLedger moves up to n tally rows of the source. A row whose bucket and
// environment the target already tallies is added to that row, so the counts
// of both stay.
func moveLedger(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error) {
	var res MergeStepResult
	rows, err := readLedgerBatch(tx, spec.SourceID, n)
	if err != nil {
		return res, err
	}
	for _, lr := range rows {
		env := lr.env
		if env == "" {
			env = spec.Environment
		}
		var present int
		err := tx.QueryRow(`SELECT count(*) FROM span_ledger WHERE bucket = ? AND project_id = ? AND environment = ?`,
			lr.bucket, spec.TargetID, env).Scan(&present)
		if err != nil {
			return res, err
		}
		if _, err := tx.Exec(`INSERT INTO span_ledger (bucket, project_id, environment, `+ledgerColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (bucket, project_id, environment) DO UPDATE SET
			accepted = accepted + excluded.accepted, sampled_out = sampled_out + excluded.sampled_out,
			shed_unsampled = shed_unsampled + excluded.shed_unsampled, lost_in_buffer = lost_in_buffer + excluded.lost_in_buffer,
			forwarded = forwarded + excluded.forwarded, processed = processed + excluded.processed, failed = failed + excluded.failed`,
			lr.bucket, spec.TargetID, env,
			lr.counts[0], lr.counts[1], lr.counts[2], lr.counts[3], lr.counts[4], lr.counts[5], lr.counts[6]); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`DELETE FROM span_ledger WHERE bucket = ? AND project_id = ? AND environment = ?`,
			lr.bucket, spec.SourceID, lr.env); err != nil {
			return res, err
		}
		res.Moved++
		res.Collided += int64(present)
		res.LastMoved = max(res.LastMoved, lr.bucket)
	}
	return res, nil
}

func readLedgerBatch(tx *sql.Tx, projectID int64, n int) ([]ledgerRow, error) {
	q, err := tx.Query(`SELECT bucket, environment, `+ledgerColumns+` FROM span_ledger
		WHERE project_id = ? ORDER BY bucket, environment LIMIT ?`, projectID, n)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var out []ledgerRow
	for q.Next() {
		var lr ledgerRow
		c := &lr.counts
		if err := q.Scan(&lr.bucket, &lr.env, &c[0], &c[1], &c[2], &c[3], &c[4], &c[5], &c[6]); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, q.Err()
}
