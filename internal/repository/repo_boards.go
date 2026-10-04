package repository

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

// ErrNotFound reports that a board, panel or query does not exist.
var ErrNotFound = errors.New("not found")

// ErrPanelSet reports a panel order that does not list the panels of the board exactly once.
var ErrPanelSet = errors.New("order must list every panel of the board once")

// BoardPanel is one query on a board. The query itself lives in saved_queries.
type BoardPanel struct {
	ID           int64      `json:"id"`
	BoardID      int64      `json:"boardId"`
	SavedQueryID int64      `json:"savedQueryId"`
	Title        string     `json:"title"`
	View         string     `json:"view"`
	Position     int        `json:"position"`
	Query        SavedQuery `json:"query"`
}

// Board is an ordered grid of panels with one shared time range.
type Board struct {
	ID             int64        `json:"id"`
	ProjectID      int64        `json:"projectId"`
	Name           string       `json:"name"`
	TimeRange      string       `json:"timeRange"`
	RefreshSeconds int          `json:"refreshSeconds"`
	Panels         []BoardPanel `json:"panels"`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

const boardColumns = `id, project_id, name, time_range, refresh_seconds, created_at, updated_at`

func (r *Repository) CreateBoard(projectID int64, name, timeRange string, refreshSeconds int) (int64, error) {
	var id int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`INSERT INTO boards (project_id, name, time_range, refresh_seconds) VALUES (?, ?, ?, ?)`,
			projectID, name, timeRange, refreshSeconds,
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

// UpdateBoard replaces the name, shared time range and refresh interval.
func (r *Repository) UpdateBoard(id int64, name, timeRange string, refreshSeconds int) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(
			`UPDATE boards SET name = ?, time_range = ?, refresh_seconds = ?, updated_at = datetime('now') WHERE id = ?`,
			name, timeRange, refreshSeconds, id,
		)
		return expectRow(res, err)
	})
}

func expectRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetBoard returns a board with its panels in order.
func (r *Repository) GetBoard(id int64) (*Board, error) {
	var b Board
	err := r.db.QueryRow(`SELECT `+boardColumns+` FROM boards WHERE id = ?`, id).
		Scan(&b.ID, &b.ProjectID, &b.Name, &b.TimeRange, &b.RefreshSeconds, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	panels, err := r.boardPanels(`WHERE p.board_id = ?`, id)
	if err != nil {
		return nil, err
	}
	b.Panels = panels
	return &b, nil
}

// ListBoards returns the boards of a project, newest first, without panels.
func (r *Repository) ListBoards(projectID int64) ([]Board, error) {
	rows, err := r.db.Query(
		`SELECT `+boardColumns+` FROM boards WHERE project_id = ? ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Board
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.ProjectID, &b.Name, &b.TimeRange, &b.RefreshSeconds, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		b.Panels = []BoardPanel{}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *Repository) boardPanels(where string, args ...any) ([]BoardPanel, error) {
	rows, err := r.db.Query(
		`SELECT p.id, p.board_id, p.saved_query_id, p.title, p.view, p.position,
			q.id, q.project_id, q.name, q.service, q.operation, q.status, q.min_duration_us, q.filters, q.definition, q.created_at
		FROM board_panels p JOIN saved_queries q ON q.id = p.saved_query_id `+where+` ORDER BY p.position, p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BoardPanel{}
	for rows.Next() {
		var p BoardPanel
		var filters, definition string
		q := &p.Query
		if err := rows.Scan(&p.ID, &p.BoardID, &p.SavedQueryID, &p.Title, &p.View, &p.Position,
			&q.ID, &q.ProjectID, &q.Name, &q.Service, &q.Operation, &q.Status, &q.MinDurationUs, &filters, &definition, &q.CreatedAt); err != nil {
			return nil, err
		}
		if filters != "" {
			q.Filters = []byte(filters)
		}
		if definition != "" {
			q.Definition = []byte(definition)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteBoard removes a board, its panels and the saved queries only those
// panels used.
func (r *Repository) DeleteBoard(id int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		return r.inTx(db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM boards WHERE id = ?`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			if _, err := tx.Exec(`DELETE FROM board_panels WHERE board_id = ?`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM boards WHERE id = ?`, id); err != nil {
				return err
			}
			return deleteOrphanBoardQueries(tx)
		})
	})
}

// deleteOrphanBoardQueries drops board queries (definition set) that no panel
// uses any more. A plain trace filter is never touched.
func deleteOrphanBoardQueries(tx *sql.Tx) error {
	_, err := tx.Exec(`DELETE FROM saved_queries WHERE definition != ''
		AND id NOT IN (SELECT saved_query_id FROM board_panels)`)
	return err
}

func (r *Repository) inTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// AddPanelWithQuery stores a query and appends a panel for it to the board in
// one transaction ("save to board"). The query takes the board's project.
func (r *Repository) AddPanelWithQuery(boardID int64, q SavedQuery, title, view string) (int64, error) {
	var panelID int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		return r.inTx(db, func(tx *sql.Tx) error {
			var projectID int64
			if err := tx.QueryRow(`SELECT project_id FROM boards WHERE id = ?`, boardID).Scan(&projectID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			q.ProjectID = projectID
			res, err := tx.Exec(insertSavedQuery, savedQueryArgs(q)...)
			if err != nil {
				return err
			}
			qid, _ := res.LastInsertId()
			var pos int
			if err := tx.QueryRow(`SELECT COALESCE(MAX(position), -1) + 1 FROM board_panels WHERE board_id = ?`, boardID).Scan(&pos); err != nil {
				return err
			}
			res, err = tx.Exec(
				`INSERT INTO board_panels (board_id, saved_query_id, title, view, position) VALUES (?, ?, ?, ?, ?)`,
				boardID, qid, title, view, pos)
			if err != nil {
				return err
			}
			panelID, _ = res.LastInsertId()
			_, err = tx.Exec(`UPDATE boards SET updated_at = datetime('now') WHERE id = ?`, boardID)
			return err
		})
	})
	return panelID, err
}

// UpdatePanel changes the title and view of a panel on a board.
func (r *Repository) UpdatePanel(boardID, panelID int64, title, view string) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(`UPDATE board_panels SET title = ?, view = ? WHERE id = ? AND board_id = ?`, title, view, panelID, boardID)
		return expectRow(res, err)
	})
}

// DeletePanel removes a panel and its query when nothing else uses it.
func (r *Repository) DeletePanel(boardID, panelID int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		return r.inTx(db, func(tx *sql.Tx) error {
			res, err := tx.Exec(`DELETE FROM board_panels WHERE id = ? AND board_id = ?`, panelID, boardID)
			if err := expectRow(res, err); err != nil {
				return err
			}
			return deleteOrphanBoardQueries(tx)
		})
	})
}

// ReorderPanels sets the panel order. ids must name exactly the panels of the board.
func (r *Repository) ReorderPanels(boardID int64, ids []int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		return r.inTx(db, func(tx *sql.Tx) error {
			existing, err := panelIDs(tx, boardID)
			if err != nil {
				return err
			}
			if !sameIDs(existing, ids) {
				return ErrPanelSet
			}
			for pos, id := range ids {
				if _, err := tx.Exec(`UPDATE board_panels SET position = ? WHERE id = ? AND board_id = ?`, pos, id, boardID); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func panelIDs(tx *sql.Tx, boardID int64) ([]int64, error) {
	rows, err := tx.Query(`SELECT id FROM board_panels WHERE board_id = ?`, boardID)
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

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]int64{}, a...)
	y := append([]int64{}, b...)
	sort.Slice(x, func(i, j int) bool { return x[i] < x[j] })
	sort.Slice(y, func(i, j int) bool { return y[i] < y[j] })
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
