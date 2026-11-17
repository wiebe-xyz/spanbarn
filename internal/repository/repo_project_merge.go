package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
	Family string
	Table  string
	// Shard is the start date of the time shard a step ran in, empty for a
	// table in a single file.
	Shard     string
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
	fam    engine.Family
	table  string
	// shardStart is the period start of the shard file the step runs in; the
	// zero time means the family's one file.
	shardStart time.Time
	// batch moves at most n rows in tx and reports what it moved and collided.
	batch func(tx *sql.Tx, spec MergeSpec, n int) (MergeStepResult, error)
}

func (s mergeStep) shardLabel() string {
	if s.shardStart.IsZero() {
		return ""
	}
	return s.shardStart.Format(time.DateOnly)
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
	plan, err := r.mergePlan(ctx)
	if err != nil {
		return nil, err
	}
	for _, step := range plan {
		total := MergeStepResult{Family: step.family, Table: step.table, Shard: step.shardLabel()}
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
	if err := r.archiveMergedSource(spec); err != nil {
		return out, fmt.Errorf("archive %s: %w", spec.SourceSlug, err)
	}
	return out, nil
}

// archiveMergedSource marks the source archived and its merge done once a full
// pass found nothing left to move in any step, which is the proof the source
// holds no rows. The project row, its id and its slug stay.
func (r *Repository) archiveMergedSource(spec MergeSpec) error {
	return r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`UPDATE projects SET archived_at = COALESCE(archived_at, datetime('now')) WHERE id = ?`, spec.SourceID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE project_merges SET state = 'done', updated_at = datetime('now')
			WHERE source_id = ? AND target_id = ? AND table_name = ''`, spec.SourceID, spec.TargetID); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ArchivedInto returns the project an archived source was merged into, or 0
// when the project is not archived. Only a finished merge resolves: while one
// runs, the source still holds rows of its own.
func (r *Repository) ArchivedInto(sourceID int64) (int64, error) {
	var target int64
	err := r.db.QueryRow(`SELECT m.target_id FROM project_merges m JOIN projects p ON p.id = m.source_id
		WHERE m.source_id = ? AND m.table_name = '' AND m.state = 'done' AND p.archived_at IS NOT NULL LIMIT 1`, sourceID).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return target, err
}

// runMergeBatch moves one batch in the step's family. A core step records its
// progress in the same transaction. Another family lives in another file, which
// cannot commit together with main, so its progress is written right after the
// batch commits: a kill between the two loses that batch's count and leaves the
// rows moved, and the next run counts only what it moves itself. Family writes
// queue behind ingest, so a merge never takes the slot ahead of a flush.
func (r *Repository) runMergeBatch(ctx context.Context, step mergeStep, spec MergeSpec, n int) (MergeStepResult, error) {
	var res MergeStepResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	inCore := step.fam == engine.FamilyCore
	write := r.execHigh
	if !inCore {
		write = r.execLow
	}
	if !step.shardStart.IsZero() {
		write = func(f engine.Family, fn func(db *sql.DB) error) error {
			return r.shards.OnShard(f, step.shardStart, "projectMerge", fn)
		}
	}
	err := write(step.fam, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if res, err = step.batch(tx, spec, n); err != nil {
			return err
		}
		res.Family, res.Table, res.Shard = step.family, step.table, step.shardLabel()
		if inCore && res.Moved+res.Collided > 0 {
			if err := recordMergeProgress(tx, spec, res); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil || inCore || res.Moved+res.Collided == 0 {
		return res, err
	}
	return res, r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := recordMergeProgress(tx, spec, res); err != nil {
			return err
		}
		return tx.Commit()
	})
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
		(source_id, target_id, environment, family, table_name, shard, cursor, rows_moved, rows_collided)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, target_id, family, table_name, shard) DO UPDATE SET
			cursor = max(cursor, excluded.cursor),
			rows_moved = rows_moved + excluded.rows_moved,
			rows_collided = rows_collided + excluded.rows_collided,
			updated_at = datetime('now')`,
		spec.SourceID, spec.TargetID, spec.Environment, res.Family, res.Table, res.Shard, res.LastMoved, res.Moved, res.Collided)
	return err
}

// MergeProgress returns the stored progress rows of a source, run row first.
func (r *Repository) MergeProgress(sourceID int64) ([]MergeStepResult, error) {
	rows, err := r.db.Query(`SELECT family, table_name, shard, rows_moved, rows_collided, cursor
		FROM project_merges WHERE source_id = ? ORDER BY table_name <> '', id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MergeStepResult
	for rows.Next() {
		var m MergeStepResult
		if err := rows.Scan(&m.Family, &m.Table, &m.Shard, &m.Moved, &m.Collided, &m.LastMoved); err != nil {
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
