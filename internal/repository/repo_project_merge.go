package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// MergeSpec names one merge of #558: the rows and keys of Source move to Target,
// and rows that carry no environment of their own get Environment.
type MergeSpec struct {
	SourceID    int64
	TargetID    int64
	SourceSlug  string // suffix for a settings row whose name the target already uses
	Environment string
}

// MergeStepResult is what a merge moved in one table. Collided counts rows the
// target already held an equal of (a duplicate pin, a duplicate exclusion) or
// rows that were renamed to avoid a clash.
type MergeStepResult struct {
	Family    string
	Table     string
	Moved     int64
	Collided  int64
	LastMoved int64
}

// MergeOptions tunes a run. Batch is the rows one write moves. OnBatch runs
// after each committed batch; an error from it stops the run, which a second
// call resumes.
type MergeOptions struct {
	Batch   int
	OnBatch func(MergeStepResult) error
}

// ErrMergeConflict is returned when a source is already merging elsewhere or a
// merge names the same project twice.
var ErrMergeConflict = errors.New("merge conflicts with an earlier merge")

type mergeStep struct {
	family string
	table  string
	// batch moves at most n rows in tx and reports what it moved and collided.
	batch func(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error)
}

// MergeProject moves the core family of the source into the target in batches:
// one transaction per batch moves rows and adds to the progress row, so a stop
// at any point leaves counts that match the rows. Every step runs again after a
// restart and moves what is left. Rows keep their ids and nothing is deleted
// except a configuration row the target already holds an equal of.
func (r *Repository) MergeProject(ctx context.Context, spec MergeSpec, opts MergeOptions) ([]MergeStepResult, error) {
	if opts.Batch <= 0 {
		opts.Batch = 500
	}
	if err := r.beginMerge(spec); err != nil {
		return nil, err
	}
	var out []MergeStepResult
	for _, step := range coreMergeSteps {
		total := MergeStepResult{Family: step.family, Table: step.table}
		for {
			res, err := r.runMergeBatch(ctx, step, spec, opts.Batch)
			if err != nil {
				return out, fmt.Errorf("merge %s: %w", step.table, err)
			}
			if res.Moved == 0 && res.Collided == 0 {
				break
			}
			total.Moved += res.Moved
			total.Collided += res.Collided
			total.LastMoved = max(total.LastMoved, res.LastMoved)
			if opts.OnBatch != nil {
				if err := opts.OnBatch(res); err != nil {
					return append(out, total), err
				}
			}
		}
		out = append(out, total)
	}
	return out, nil
}

func (r *Repository) runMergeBatch(ctx context.Context, step mergeStep, spec MergeSpec, n int) (MergeStepResult, error) {
	var res MergeStepResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	err := r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if res, err = step.batch(tx, spec, n); err != nil {
			return err
		}
		res.Family, res.Table = step.family, step.table
		if res.Moved+res.Collided > 0 {
			if err := recordMergeProgress(tx, spec, res); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	return res, err
}

// beginMerge writes the run row, and refuses a source that already merges into
// another target or with another environment, so one source cannot be split.
func (r *Repository) beginMerge(spec MergeSpec) error {
	if spec.SourceID == spec.TargetID {
		return fmt.Errorf("%w: source and target are the same project", ErrMergeConflict)
	}
	return r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		var target int64
		var env string
		err := db.QueryRow(`SELECT target_id, environment FROM project_merges
			WHERE source_id = ? AND table_name = '' LIMIT 1`, spec.SourceID).Scan(&target, &env)
		switch {
		case err == nil && (target != spec.TargetID || env != spec.Environment):
			return fmt.Errorf("%w: project %d already merges into project %d with environment %q",
				ErrMergeConflict, spec.SourceID, target, env)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = db.Exec(`INSERT OR IGNORE INTO project_merges (source_id, target_id, environment, state)
			VALUES (?, ?, ?, 'running')`, spec.SourceID, spec.TargetID, spec.Environment)
		return err
	})
}

func recordMergeProgress(tx *sql.Tx, spec MergeSpec, res MergeStepResult) error {
	_, err := tx.Exec(`INSERT INTO project_merges
		(source_id, target_id, environment, family, table_name, cursor, rows_moved, rows_collided)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, target_id, family, table_name, shard) DO UPDATE SET
			cursor = max(cursor, excluded.cursor),
			rows_moved = rows_moved + excluded.rows_moved,
			rows_collided = rows_collided + excluded.rows_collided,
			updated_at = datetime('now')`,
		spec.SourceID, spec.TargetID, spec.Environment, res.Family, res.Table, res.LastMoved, res.Moved, res.Collided)
	return err
}

// MergeProgress returns the stored progress rows of a source, run row first.
func (r *Repository) MergeProgress(sourceID int64) ([]MergeStepResult, error) {
	rows, err := r.db.Query(`SELECT family, table_name, rows_moved, rows_collided, cursor
		FROM project_merges WHERE source_id = ? ORDER BY table_name <> '', id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MergeStepResult
	for rows.Next() {
		var m MergeStepResult
		if err := rows.Scan(&m.Family, &m.Table, &m.Moved, &m.Collided, &m.LastMoved); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MergedInto returns the project a source was merged into, or 0 when it has not
// been merged. Old links resolve through it.
func (r *Repository) MergedInto(sourceID int64) (int64, error) {
	var target int64
	err := r.db.QueryRow(`SELECT target_id FROM project_merges
		WHERE source_id = ? AND table_name = '' LIMIT 1`, sourceID).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return target, err
}
