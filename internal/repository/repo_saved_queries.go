package repository

import (
	"database/sql"
	"encoding/json"
	"time"
)

// SavedQuery is a named query. Filters holds the shared filter model
// (internal/filter) as JSON. The four legacy fields stay for clients and rows
// that predate it; migration 036 maps them into Filters. Definition holds the
// rest of a board query (group by, calculations, order, limit, sample) and is
// empty for a plain trace filter.
type SavedQuery struct {
	ID            int64           `json:"id"`
	ProjectID     int64           `json:"projectId"`
	Name          string          `json:"name"`
	Service       string          `json:"service"`
	Operation     string          `json:"operation"`
	Status        string          `json:"status"`
	MinDurationUs int64           `json:"minDurationUs"`
	Filters       json.RawMessage `json:"filters"`
	Definition    json.RawMessage `json:"definition,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

const savedQueryColumns = `id, project_id, name, service, operation, status, min_duration_us, filters, definition, created_at`

func scanSavedQuery(s rowScanner) (SavedQuery, error) {
	var q SavedQuery
	var filters, definition string
	if err := s.Scan(&q.ID, &q.ProjectID, &q.Name, &q.Service, &q.Operation, &q.Status, &q.MinDurationUs, &filters, &definition, &q.CreatedAt); err != nil {
		return q, err
	}
	if filters != "" {
		q.Filters = json.RawMessage(filters)
	}
	if definition != "" {
		q.Definition = json.RawMessage(definition)
	}
	return q, nil
}

const insertSavedQuery = `INSERT INTO saved_queries
	(project_id, name, service, operation, status, min_duration_us, filters, definition)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

func savedQueryArgs(q SavedQuery) []any {
	return []any{q.ProjectID, q.Name, q.Service, q.Operation, q.Status, q.MinDurationUs, string(q.Filters), string(q.Definition)}
}

func (r *Repository) CreateSavedQuery(q SavedQuery) (int64, error) {
	var id int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(insertSavedQuery, savedQueryArgs(q)...)
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
		`SELECT `+savedQueryColumns+` FROM saved_queries WHERE project_id = ? ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SavedQuery
	for rows.Next() {
		q, err := scanSavedQuery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteSavedQuery(id int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		return r.inTx(db, func(tx *sql.Tx) error {
			// A panel without its query cannot render, so it goes with it.
			if _, err := tx.Exec("DELETE FROM board_panels WHERE saved_query_id = ?", id); err != nil {
				return err
			}
			_, err := tx.Exec("DELETE FROM saved_queries WHERE id = ?", id)
			return err
		})
	})
}
