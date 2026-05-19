package repository

import "time"

func (r *Repository) ListTraceExclusions(projectID int64) ([]TraceExclusion, error) {
	rows, err := r.db.Query(
		`SELECT id, project_id, operation, created_at FROM trace_exclusions WHERE project_id = ? ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceExclusion
	for rows.Next() {
		var e TraceExclusion
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Operation, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *Repository) CreateTraceExclusion(projectID int64, operation string) (int64, error) {
	res, err := r.db.Exec(
		`INSERT OR IGNORE INTO trace_exclusions (project_id, operation, created_at) VALUES (?, ?, ?)`,
		projectID, operation, time.Now().UTC(),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) DeleteTraceExclusion(id int64) error {
	_, err := r.db.Exec(`DELETE FROM trace_exclusions WHERE id = ?`, id)
	return err
}

// ExcludedOperations returns just the operation strings for a project, used
// to automatically inject exclusions into trace search queries.
func (r *Repository) ExcludedOperations(projectID int64) ([]string, error) {
	rows, err := r.db.Query(
		`SELECT operation FROM trace_exclusions WHERE project_id = ? ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []string
	for rows.Next() {
		var op string
		if err := rows.Scan(&op); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
