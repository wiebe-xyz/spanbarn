package repository

import (
	"database/sql"
	"fmt"
)

func (r *Repository) CreateProject(slug, name string) (Project, error) {
	res, err := r.db.Exec("INSERT INTO projects (slug, name) VALUES (?, ?)", slug, name)
	if err != nil {
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return r.getProjectByID(id)
}

func (r *Repository) getProjectByID(id int64) (Project, error) {
	var p Project
	err := r.db.QueryRow("SELECT id, slug, name, status, created_at FROM projects WHERE id = ?", id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetProjectBySlug(slug string) (Project, error) {
	var p Project
	err := r.db.QueryRow("SELECT id, slug, name, status, created_at FROM projects WHERE slug = ?", slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt)
	return p, err
}

func (r *Repository) ListProjects() ([]Project, error) {
	rows, err := r.db.Query("SELECT id, slug, name, status, created_at FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) ListProjectIDs() ([]int64, error) {
	rows, err := r.db.Query("SELECT id FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) EnsureProjectPending(slug, name string) (Project, error) {
	p, err := r.GetProjectBySlug(slug)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return Project{}, err
	}
	res, err := r.db.Exec("INSERT INTO projects (slug, name, status) VALUES (?, ?, 'pending')", slug, name)
	if err != nil {
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return r.getProjectByID(id)
}

func (r *Repository) ApproveProject(id int64) (Project, error) {
	res, err := r.db.Exec("UPDATE projects SET status = 'active' WHERE id = ?", id)
	if err != nil {
		return Project{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Project{}, sql.ErrNoRows
	}
	return r.getProjectByID(id)
}

func (r *Repository) DeleteProject(id int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM api_keys WHERE project_id = ?", id); err != nil {
		return err
	}
	res, err := tx.Exec("DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ProjectUsageStats holds per-project span metrics.
type ProjectUsageStats struct {
	ProjectID       int64 `json:"projectId"`
	SpanCount       int64 `json:"spanCount"`
	ErrorCount      int64 `json:"errorCount"`
	RecentSpanCount int64 `json:"recentSpanCount"` // last 4 h, indicates current velocity
}

// ProjectUsageStatsAll returns span count and error count per project for the last N hours.
// Reads from raw spans via idx_spans_project_stats (ingested_at, project_id, status) — a
// covering index that satisfies the full query without touching main table rows.
// TODO: once aggregation runs continuously (not only at retention time), switch this to
// query the aggregates table for O(N_buckets) instead of O(N_raw_spans) performance.
func (r *Repository) ProjectUsageStatsAll(hours int) ([]ProjectUsageStats, error) {
	rows, err := r.db.Query(`
		SELECT
			project_id,
			COUNT(*)                                                                          AS span_count,
			SUM(CASE WHEN status IN ('error','ERROR','Error') THEN 1 ELSE 0 END)             AS error_count,
			SUM(CASE WHEN ingested_at >= datetime('now', '-4 hours') THEN 1 ELSE 0 END)      AS recent_span_count
		FROM spans
		WHERE ingested_at >= datetime('now', ?)
		GROUP BY project_id
		ORDER BY span_count DESC`,
		fmt.Sprintf("-%d hours", hours))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectUsageStats
	for rows.Next() {
		var s ProjectUsageStats
		if err := rows.Scan(&s.ProjectID, &s.SpanCount, &s.ErrorCount, &s.RecentSpanCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repository) EnsureSetupAPIKey(projectID int64, keySHA256 string) error {
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO api_keys (project_id, name, key_hash, scope) VALUES (?, 'setup', ?, 'ingest')`,
		projectID, keySHA256,
	)
	return err
}
