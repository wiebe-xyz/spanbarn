package repository

import (
	"encoding/json"
	"time"
)

// SavedQuery is a named trace filter. Filters holds the shared filter model
// (internal/filter) as JSON. The four legacy fields stay for clients and rows
// that predate it; migration 036 maps them into Filters.
type SavedQuery struct {
	ID            int64           `json:"id"`
	ProjectID     int64           `json:"projectId"`
	Name          string          `json:"name"`
	Service       string          `json:"service"`
	Operation     string          `json:"operation"`
	Status        string          `json:"status"`
	MinDurationUs int64           `json:"minDurationUs"`
	Filters       json.RawMessage `json:"filters"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func (r *Repository) CreateSavedQuery(q SavedQuery) (int64, error) {
	var id int64
	err := r.execHigh(func() error {
		res, e := r.db.Exec(`INSERT INTO saved_queries
			(project_id, name, service, operation, status, min_duration_us, filters)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			q.ProjectID, q.Name, q.Service, q.Operation, q.Status, q.MinDurationUs, string(q.Filters),
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

func (r *Repository) ListSavedQueries(projectID int64) ([]SavedQuery, error) {
	rows, err := r.db.Query(
		`SELECT id, project_id, name, service, operation, status, min_duration_us, filters, created_at
		FROM saved_queries WHERE project_id = ? ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SavedQuery
	for rows.Next() {
		var q SavedQuery
		var filters string
		if err := rows.Scan(&q.ID, &q.ProjectID, &q.Name, &q.Service, &q.Operation, &q.Status, &q.MinDurationUs, &filters, &q.CreatedAt); err != nil {
			return nil, err
		}
		if filters != "" {
			q.Filters = json.RawMessage(filters)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteSavedQuery(id int64) error {
	return r.execHigh(func() error {
		_, err := r.db.Exec("DELETE FROM saved_queries WHERE id = ?", id)
		return err
	})
}
