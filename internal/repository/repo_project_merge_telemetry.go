package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// telemetryMergeSteps are the logs, metrics and prompts of a project. The
// families live in time shards when sharding is on, so each shard is a step of
// its own; without shards the family's one file is one step.
var telemetryMergeSteps = []struct {
	fam   engine.Family
	table string
	opts  moveOpts
}{
	{engine.FamilyLogs, "logs", moveOpts{stamp: true}},
	{engine.FamilyMetrics, "metrics", moveOpts{stamp: true}},
	{engine.FamilyPrompts, "prompt_records", moveOpts{}},
}

// coreTelemetryMergeSteps are the telemetry tables that live in the main file:
// the logs kept past their shard and the two metric rollup tiers.
var coreTelemetryMergeSteps = []mergeStep{
	{family: "core", fam: engine.FamilyCore, table: "kept_logs", batch: moveByID("kept_logs", moveOpts{stamp: true})},
	{family: "core", fam: engine.FamilyCore, table: "metric_rollups", batch: moveRollups("metric_rollups", false)},
	{family: "core", fam: engine.FamilyCore, table: "metric_rollups_coarse", batch: moveRollups("metric_rollups_coarse", true)},
}

// mergePlan lists every step of a merge in order. The sharded families add one
// step per active shard, oldest first, read from the shard list when the plan
// is made, so a run that starts after a new day opened picks the new shard up.
func (r *Repository) mergePlan(ctx context.Context) ([]mergeStep, error) {
	plan := append([]mergeStep{}, fixedMergeSteps...)
	plan = append(plan, coreTelemetryMergeSteps...)
	var rows []engine.ShardRow
	if r.shards != nil {
		var err error
		if rows, err = r.shards.Rows(ctx); err != nil {
			return nil, fmt.Errorf("list shards: %w", err)
		}
	}
	for _, t := range telemetryMergeSteps {
		step := mergeStep{family: t.fam.String(), fam: t.fam, table: t.table, batch: moveByID(t.table, t.opts)}
		if !r.shards.Sharded(t.fam) {
			plan = append(plan, step)
			continue
		}
		for _, row := range rows {
			if row.Family != t.fam || row.State != engine.ShardActive {
				continue
			}
			s := step
			s.shardStart = row.Start
			plan = append(plan, s)
		}
	}
	return plan, nil
}

// rollupColumns is every column of a rollup row but its id.
const rollupColumns = `name, type, unit, temporality, attr_fingerprint, attributes, bucket, count, sum, min, max, last, obs_count, extra`

// moveRollups moves up to n rows of a metric rollup tier. The environment is a
// label of the series, so a row without the label gets it stamped, which gives
// the series a new fingerprint. A row whose new key the target already holds is
// folded into that row: counts and sums add, the range widens, and the target
// keeps its own last value and snapshot (the target's rows are the newer ones,
// as the keys moved first). Rows keep their ids otherwise.
func moveRollups(table string, coarse bool) func(*sql.Tx, MergeSpec, int) (MergeStepResult, error) {
	return func(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error) {
		var res MergeStepResult
		type rollup struct {
			id                           int64
			name, typ, unit, temporality string
			fingerprint, attributes      string
			stepSeconds                  int64
			count, obs                   int64
			sum, lo, hi, last            float64
			extra                        sql.NullString
		}
		stepCol, stepKey := "0", ""
		if coarse {
			stepCol, stepKey = "step_seconds", " AND step_seconds = ?"
		}
		rows, err := tx.Query(fmt.Sprintf(`SELECT id, %s, %s FROM %s WHERE project_id = ? ORDER BY id LIMIT ?`, stepCol, rollupColumns, table),
			spec.SourceID, n)
		if err != nil {
			return res, err
		}
		var batch []rollup
		for rows.Next() {
			var m rollup
			var bucket any
			if err := rows.Scan(&m.id, &m.stepSeconds, &m.name, &m.typ, &m.unit, &m.temporality, &m.fingerprint, &m.attributes, &bucket,
				&m.count, &m.sum, &m.lo, &m.hi, &m.last, &m.obs, &m.extra); err != nil {
				rows.Close()
				return res, err
			}
			batch = append(batch, m)
		}
		if err := rows.Close(); err != nil {
			return res, err
		}
		stamp := spec.Environment != filter.ProductionEnvironment
		for _, m := range batch {
			if stamp {
				labels := metrics.ParseAttributes([]byte(m.attributes))
				if labels["deployment.environment"] == "" {
					labels["deployment.environment"] = spec.Environment
					m.attributes, m.fingerprint = metrics.CanonicalAttributes(labels), metrics.Fingerprint(labels)
				}
			}
			var held int64
			err := tx.QueryRow(fmt.Sprintf(`SELECT id FROM %[1]s WHERE project_id = ? AND name = ? AND attr_fingerprint = ? AND bucket = (SELECT bucket FROM %[1]s WHERE id = ?)`+stepKey, table),
				append([]any{spec.TargetID, m.name, m.fingerprint, m.id}, stepArgs(coarse, m.stepSeconds)...)...).Scan(&held)
			switch {
			case err == sql.ErrNoRows:
				if _, err := tx.Exec(fmt.Sprintf(`UPDATE %s SET project_id = ?, attributes = ?, attr_fingerprint = ? WHERE id = ?`, table),
					spec.TargetID, m.attributes, m.fingerprint, m.id); err != nil {
					return res, err
				}
			case err != nil:
				return res, err
			default:
				if _, err := tx.Exec(fmt.Sprintf(`UPDATE %s SET count = count + ?, sum = sum + ?, min = MIN(min, ?), max = MAX(max, ?),
					obs_count = obs_count + ?, extra = COALESCE(NULLIF(extra, ''), ?) WHERE id = ?`, table),
					m.count, m.sum, m.lo, m.hi, m.obs, m.extra, held); err != nil {
					return res, err
				}
				if _, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, table), m.id); err != nil {
					return res, err
				}
				res.Collided++
			}
			res.Moved++
			res.LastMoved = max(res.LastMoved, m.id)
		}
		return res, nil
	}
}

func stepArgs(coarse bool, step int64) []any {
	if coarse {
		return []any{step}
	}
	return nil
}
