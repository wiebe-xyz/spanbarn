package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrConflict reports a write that collides with an existing row, such as an
// SLO name already used in the project.
var ErrConflict = errors.New("conflict")

const sloColumns = `id, project_id, name, good_filter, total_filter, target, window_days, created_at`

const sloBurnAlertColumns = `a.id, a.slo_id, a.window_minutes, a.burn_rate, a.webhook_url, a.email,
	a.cooldown_minutes, a.enabled, a.firing, a.last_triggered_at`

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// wrapSLOWriteErr maps a SQLite unique violation to ErrConflict.
func wrapSLOWriteErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func jsonOrEmpty(b []byte) string {
	if strings.TrimSpace(string(b)) == "" {
		return "{}"
	}
	return string(b)
}

func scanSLO(row rowScanner) (SLO, error) {
	var s SLO
	var good, total string
	err := row.Scan(&s.ID, &s.ProjectID, &s.Name, &good, &total, &s.Target, &s.WindowDays, &s.CreatedAt)
	s.GoodFilter, s.TotalFilter = []byte(good), []byte(total)
	return s, err
}

// ListSLOs returns the SLOs of a project ordered by id.
func (r *Repository) ListSLOs(projectID int64) ([]SLO, error) {
	rows, err := r.db.Query(`SELECT `+sloColumns+` FROM slos WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SLO
	for rows.Next() {
		s, err := scanSLO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSLO returns one SLO of a project, or ErrNotFound.
func (r *Repository) GetSLO(projectID, id int64) (*SLO, error) {
	s, err := scanSLO(r.db.QueryRow(
		`SELECT `+sloColumns+` FROM slos WHERE project_id = ? AND id = ?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSLO inserts an SLO. A name already used in the project returns ErrConflict.
func (r *Repository) CreateSLO(s SLO) (int64, error) {
	var id int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`INSERT INTO slos (project_id, name, good_filter, total_filter, target, window_days)
			VALUES (?, ?, ?, ?, ?, ?)`,
			s.ProjectID, s.Name, jsonOrEmpty(s.GoodFilter), jsonOrEmpty(s.TotalFilter), s.Target, s.WindowDays,
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, wrapSLOWriteErr(err)
}

// UpdateSLO replaces the editable fields of an SLO of the project.
func (r *Repository) UpdateSLO(s SLO) error {
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`UPDATE slos SET name = ?, good_filter = ?, total_filter = ?, target = ?, window_days = ?
			WHERE project_id = ? AND id = ?`,
			s.Name, jsonOrEmpty(s.GoodFilter), jsonOrEmpty(s.TotalFilter), s.Target, s.WindowDays,
			s.ProjectID, s.ID,
		)
		return expectRow(res, e)
	})
	return wrapSLOWriteErr(err)
}

// DeleteSLO removes an SLO of the project. Its burn alerts and counts go with it.
func (r *Repository) DeleteSLO(projectID, id int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(`DELETE FROM slos WHERE project_id = ? AND id = ?`, projectID, id)
		return expectRow(res, err)
	})
}

func scanSLOBurnAlert(row rowScanner) (SLOBurnAlert, error) {
	var a SLOBurnAlert
	var enabled, firing int
	err := row.Scan(&a.ID, &a.SLOID, &a.WindowMinutes, &a.BurnRate, &a.WebhookURL, &a.Email,
		&a.CooldownMinutes, &enabled, &firing, &a.LastTriggeredAt)
	a.Enabled, a.Firing = enabled != 0, firing != 0
	return a, err
}

// ListSLOBurnAlerts returns the burn alerts of one SLO of the project.
func (r *Repository) ListSLOBurnAlerts(projectID, sloID int64) ([]SLOBurnAlert, error) {
	return r.querySLOBurnAlerts(`WHERE s.project_id = ? AND a.slo_id = ? ORDER BY a.id`, projectID, sloID)
}

// ListEnabledSLOBurnAlerts returns every enabled burn alert across projects,
// for the evaluator.
func (r *Repository) ListEnabledSLOBurnAlerts() ([]SLOBurnAlert, error) {
	return r.querySLOBurnAlerts(`WHERE a.enabled = 1 ORDER BY a.id`)
}

func (r *Repository) querySLOBurnAlerts(where string, args ...any) ([]SLOBurnAlert, error) {
	rows, err := r.db.Query(`SELECT `+sloBurnAlertColumns+
		` FROM slo_burn_alerts a JOIN slos s ON s.id = a.slo_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SLOBurnAlert
	for rows.Next() {
		a, err := scanSLOBurnAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetSLOBurnAlert returns one burn alert of an SLO of the project, or ErrNotFound.
func (r *Repository) GetSLOBurnAlert(projectID, id int64) (*SLOBurnAlert, error) {
	a, err := scanSLOBurnAlert(r.db.QueryRow(`SELECT `+sloBurnAlertColumns+
		` FROM slo_burn_alerts a JOIN slos s ON s.id = a.slo_id WHERE s.project_id = ? AND a.id = ?`,
		projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// CreateSLOBurnAlert adds a burn alert to an SLO of the project. An SLO that is
// not in the project returns ErrNotFound.
func (r *Repository) CreateSLOBurnAlert(projectID int64, a SLOBurnAlert) (int64, error) {
	var id int64
	err := r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			`INSERT INTO slo_burn_alerts (slo_id, window_minutes, burn_rate, webhook_url, email,
				cooldown_minutes, enabled)
			SELECT id, ?, ?, ?, ?, ?, ? FROM slos WHERE id = ? AND project_id = ?`,
			a.WindowMinutes, a.BurnRate, a.WebhookURL, a.Email, a.CooldownMinutes, boolInt(a.Enabled),
			a.SLOID, projectID,
		)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

// UpdateSLOBurnAlert replaces the editable fields of a burn alert of the project.
func (r *Repository) UpdateSLOBurnAlert(projectID int64, a SLOBurnAlert) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(
			`UPDATE slo_burn_alerts SET window_minutes = ?, burn_rate = ?, webhook_url = ?, email = ?,
				cooldown_minutes = ?, enabled = ?
			WHERE id = ? AND slo_id IN (SELECT id FROM slos WHERE project_id = ?)`,
			a.WindowMinutes, a.BurnRate, a.WebhookURL, a.Email, a.CooldownMinutes, boolInt(a.Enabled),
			a.ID, projectID,
		)
		return expectRow(res, err)
	})
}

// DeleteSLOBurnAlert removes a burn alert of the project.
func (r *Repository) DeleteSLOBurnAlert(projectID, id int64) error {
	return r.execHigh(FamilyCore, func(db *sql.DB) error {
		res, err := db.Exec(
			`DELETE FROM slo_burn_alerts WHERE id = ? AND slo_id IN (SELECT id FROM slos WHERE project_id = ?)`,
			id, projectID)
		return expectRow(res, err)
	})
}

// UpdateSLOBurnAlertState records whether an alert is firing and, when at is
// not zero, when it last triggered.
func (r *Repository) UpdateSLOBurnAlertState(id int64, firing bool, at time.Time) error {
	return r.execLow(FamilyCore, func(db *sql.DB) error {
		var res sql.Result
		var err error
		if at.IsZero() {
			res, err = db.Exec(`UPDATE slo_burn_alerts SET firing = ? WHERE id = ?`, boolInt(firing), id)
		} else {
			res, err = db.Exec(`UPDATE slo_burn_alerts SET firing = ?, last_triggered_at = ? WHERE id = ?`,
				boolInt(firing), at.UTC(), id)
		}
		return expectRow(res, err)
	})
}

// InsertSLOCounts writes bucket counts. A bucket already stored for the SLO is
// replaced, so recording the same tick twice leaves one row.
func (r *Repository) InsertSLOCounts(counts []SLOCount) error {
	if len(counts) == 0 {
		return nil
	}
	return r.execLow(FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmt, err := tx.Prepare(`INSERT INTO slo_counts (slo_id, bucket_start, good, total) VALUES (?, ?, ?, ?)
			ON CONFLICT(slo_id, bucket_start) DO UPDATE SET good = excluded.good, total = excluded.total`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, c := range counts {
			if _, err := stmt.Exec(c.SLOID, c.BucketStart.UTC(), c.Good, c.Total); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

// SumSLOCounts returns the good and total counts of buckets starting in [from, to).
func (r *Repository) SumSLOCounts(sloID int64, from, to time.Time) (good, total int64, err error) {
	err = r.db.QueryRow(
		`SELECT COALESCE(SUM(good), 0), COALESCE(SUM(total), 0) FROM slo_counts
		WHERE slo_id = ? AND bucket_start >= ? AND bucket_start < ?`,
		sloID, from.UTC(), to.UTC(),
	).Scan(&good, &total)
	return good, total, err
}

// LatestSLOBucket returns the start of the newest stored bucket, or the zero
// time when the SLO has none.
func (r *Repository) LatestSLOBucket(sloID int64) (time.Time, error) {
	var t time.Time
	err := r.db.QueryRow(
		`SELECT bucket_start FROM slo_counts WHERE slo_id = ? ORDER BY bucket_start DESC LIMIT 1`, sloID,
	).Scan(&t)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return t, err
}

// DeleteSLOCountsBefore prunes buckets starting before cutoff across all SLOs
// and returns how many rows it removed.
func (r *Repository) DeleteSLOCountsBefore(cutoff time.Time) (int64, error) {
	return r.execLowAffecting(FamilyCore, `DELETE FROM slo_counts WHERE bucket_start < ?`, cutoff.UTC())
}
