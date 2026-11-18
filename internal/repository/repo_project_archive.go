package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// ErrProjectNotEmpty is returned when a project still has a key or a row in a
// table a merge would move.
var ErrProjectNotEmpty = errors.New("project holds keys or rows")

// ArchiveEmptyProject sets archived_at on a project that holds nothing (#558):
// no ingest key and no row in any table the merge plan covers, shards included.
// It keeps the project row, so the id and slug stay taken, and a project with
// data refuses, which keeps an archive free of data moves and reversible by
// clearing archived_at.
func (r *Repository) ArchiveEmptyProject(ctx context.Context, id int64) error {
	plan, err := r.mergePlan(ctx)
	if err != nil {
		return err
	}
	for _, step := range plan {
		var held bool
		err := r.stepWriter(step)(step.fam, func(db *sql.DB) error {
			return db.QueryRow(`SELECT EXISTS (SELECT 1 FROM "`+step.table+`" WHERE project_id = ?)`, id).Scan(&held)
		})
		if err != nil {
			return fmt.Errorf("check %s: %w", step.table, err)
		}
		if held {
			return fmt.Errorf("%w: %s", ErrProjectNotEmpty, step.table)
		}
	}
	return r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		_, err := db.Exec(`UPDATE projects SET archived_at = COALESCE(archived_at, datetime('now')) WHERE id = ?`, id)
		return err
	})
}
