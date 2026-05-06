package repository

import (
	"fmt"
	"strings"
	"time"
)

func (r *Repository) ListAlerts(projectID int64) ([]Alert, error) {
	rows, err := r.db.Query(
		`SELECT id, project_id, service, operation, type, threshold,
			comparison_window, cooldown_minutes, COALESCE(webhook_url,''), COALESCE(email,''),
			enabled, last_triggered_at, created_at
		FROM alerts WHERE project_id = ? ORDER BY id`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var enabled int
		if err := rows.Scan(
			&a.ID, &a.ProjectID, &a.Service, &a.Operation, &a.Type, &a.Threshold,
			&a.ComparisonWindow, &a.CooldownMinutes, &a.WebhookURL, &a.Email,
			&enabled, &a.LastTriggeredAt, &a.CreatedAt,
		); err != nil {
			return nil, err
		}
		a.Enabled = enabled != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) CreateAlert(a Alert) (int64, error) {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	res, err := r.db.Exec(
		`INSERT INTO alerts (project_id, service, operation, type, threshold,
			comparison_window, cooldown_minutes, webhook_url, email, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ProjectID, a.Service, a.Operation, a.Type, a.Threshold,
		a.ComparisonWindow, a.CooldownMinutes, a.WebhookURL, a.Email, enabled,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) UpdateAlert(a Alert) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	res, err := r.db.Exec(
		`UPDATE alerts SET service = ?, operation = ?, type = ?, threshold = ?,
			comparison_window = ?, cooldown_minutes = ?, webhook_url = ?, email = ?, enabled = ?
		WHERE id = ?`,
		a.Service, a.Operation, a.Type, a.Threshold,
		a.ComparisonWindow, a.CooldownMinutes, a.WebhookURL, a.Email, enabled,
		a.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("alert %d not found", a.ID)
	}
	return nil
}

func (r *Repository) DeleteAlert(id int64) error {
	res, err := r.db.Exec("DELETE FROM alerts WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("alert %d not found", id)
	}
	return nil
}

func (r *Repository) UpdateAlertLastTriggered(id int64, at time.Time) error {
	_, err := r.db.Exec("UPDATE alerts SET last_triggered_at = ? WHERE id = ?", at, id)
	return err
}

// --- Error Samples ---

func (r *Repository) InsertErrorSamples(spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO error_samples
		(project_id, trace_id, span_id, parent_span_id, name, service, resource, kind, status, start_time_us, duration_us, attributes, events)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, s := range spans {
		var parentID *string
		if s.ParentSpanID != "" {
			parentID = &s.ParentSpanID
		}
		if _, err := stmt.Exec(
			s.ProjectID, s.TraceID, s.SpanID, parentID,
			s.Name, s.Service, s.Resource, s.Kind, s.Status,
			s.StartTimeUs, s.DurationUs, s.Attributes, s.Events,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) QueryErrorSamples(f SpanFilter) ([]Span, error) {
	var where []string
	var args []any

	if f.ProjectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		where = append(where, "name = ?")
		args = append(args, f.Operation)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if !f.From.IsZero() {
		where = append(where, "sampled_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "sampled_at <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM error_samples"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY sampled_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	return r.scanErrorSamples(q, args...)
}

func (r *Repository) DeleteErrorSamplesOlderThan(cutoff time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM error_samples WHERE sampled_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) scanErrorSamples(query string, args ...any) ([]Span, error) {
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Span
	for rows.Next() {
		var s Span
		if err := rows.Scan(
			&s.ID, &s.ProjectID, &s.TraceID, &s.SpanID, &s.ParentSpanID,
			&s.Name, &s.Service, &s.Resource, &s.Kind, &s.Status,
			&s.StartTimeUs, &s.DurationUs, &s.Attributes, &s.Events, &s.IngestedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
