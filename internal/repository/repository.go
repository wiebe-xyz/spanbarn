package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Repository provides data access methods over a SQLite database.
type Repository struct {
	db *sql.DB
}

// NewRepository creates a Repository backed by the given database connection.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// DB returns the underlying *sql.DB, useful for testing.
func (r *Repository) DB() *sql.DB {
	return r.db
}

// --- Projects ---

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
	err := r.db.QueryRow("SELECT id, slug, name, created_at FROM projects WHERE id = ?", id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetProjectBySlug(slug string) (Project, error) {
	var p Project
	err := r.db.QueryRow("SELECT id, slug, name, created_at FROM projects WHERE slug = ?", slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt)
	return p, err
}

func (r *Repository) ListProjects() ([]Project, error) {
	rows, err := r.db.Query("SELECT id, slug, name, created_at FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Users ---

func (r *Repository) CreateUser(username, passwordHash string) error {
	_, err := r.db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, passwordHash)
	return err
}

func (r *Repository) GetUserByUsername(username string) (User, error) {
	var u User
	err := r.db.QueryRow("SELECT id, username, password_hash, created_at FROM users WHERE username = ?", username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	return u, err
}

func (r *Repository) DeleteUser(username string) error {
	res, err := r.db.Exec("DELETE FROM users WHERE username = ?", username)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) ListUsers() ([]User, error) {
	rows, err := r.db.Query("SELECT id, username, password_hash, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// --- API Keys ---

func (r *Repository) CreateAPIKey(projectID int64, name, keyHash, scope string) (int64, error) {
	res, err := r.db.Exec(
		"INSERT INTO api_keys (project_id, name, key_hash, scope) VALUES (?, ?, ?, ?)",
		projectID, name, keyHash, scope,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) GetAPIKeyByHash(keyHash string) (APIKey, error) {
	var k APIKey
	err := r.db.QueryRow(
		"SELECT id, project_id, name, key_hash, scope, last_used_at, created_at FROM api_keys WHERE key_hash = ?",
		keyHash,
	).Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.LastUsedAt, &k.CreatedAt)
	return k, err
}

func (r *Repository) TouchAPIKey(id int64) error {
	_, err := r.db.Exec("UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", id)
	return err
}

func (r *Repository) ListAPIKeys(projectID int64) ([]APIKey, error) {
	rows, err := r.db.Query(
		"SELECT id, project_id, name, key_hash, scope, last_used_at, created_at FROM api_keys WHERE project_id = ? ORDER BY id",
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repository) ListAllAPIKeys() ([]APIKey, error) {
	rows, err := r.db.Query(
		"SELECT id, project_id, name, key_hash, scope, last_used_at, created_at FROM api_keys ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repository) RevokeAPIKey(id int64) error {
	res, err := r.db.Exec("DELETE FROM api_keys WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// --- Spans ---

func (r *Repository) InsertSpans(spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO spans
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

func (r *Repository) QuerySpans(f SpanFilter) ([]Span, error) {
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
	if f.MinDuration > 0 {
		where = append(where, "duration_us >= ?")
		args = append(args, f.MinDuration)
	}
	if !f.From.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY ingested_at DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	return r.scanSpans(q, args...)
}

func (r *Repository) GetTraceByID(traceID string) ([]Span, error) {
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE trace_id = ? ORDER BY start_time_us",
		traceID,
	)
}

func (r *Repository) DeleteSpansByIDs(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "DELETE FROM spans WHERE id IN (" + strings.Join(placeholders, ",") + ")"
	res, err := r.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) DeleteSpansOlderThan(cutoff time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM spans WHERE ingested_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *Repository) GetSpansForAggregation(cutoff time.Time, limit int) ([]Span, error) {
	if limit <= 0 {
		limit = 1000
	}
	return r.scanSpans(
		"SELECT id, project_id, trace_id, span_id, COALESCE(parent_span_id,''), name, service, resource, kind, status, start_time_us, duration_us, attributes, events, ingested_at FROM spans WHERE ingested_at <= ? ORDER BY ingested_at LIMIT ?",
		cutoff, limit,
	)
}

func (r *Repository) scanSpans(query string, args ...any) ([]Span, error) {
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

// --- Aggregates ---

func (r *Repository) UpsertAggregate(agg Aggregate) error {
	_, err := r.db.Exec(`INSERT INTO aggregates
		(project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, service, operation, resource, kind, bucket)
		DO UPDATE SET
			count = count + excluded.count,
			error_count = error_count + excluded.error_count,
			p50_us = excluded.p50_us,
			p95_us = excluded.p95_us,
			p99_us = excluded.p99_us,
			max_us = MAX(max_us, excluded.max_us),
			sum_duration_us = sum_duration_us + excluded.sum_duration_us`,
		agg.ProjectID, agg.Service, agg.Operation, agg.Resource, agg.Kind,
		agg.Bucket, agg.Count, agg.ErrorCount,
		agg.P50Us, agg.P95Us, agg.P99Us, agg.MaxUs, agg.SumDurationUs,
	)
	return err
}

func (r *Repository) QueryAggregates(f AggregateFilter) ([]Aggregate, error) {
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
		where = append(where, "operation = ?")
		args = append(args, f.Operation)
	}
	if !f.From.IsZero() {
		where = append(where, "bucket >= ?")
		args = append(args, f.From)
	}
	if !f.To.IsZero() {
		where = append(where, "bucket <= ?")
		args = append(args, f.To)
	}

	q := "SELECT id, project_id, service, operation, resource, kind, bucket, count, error_count, p50_us, p95_us, p99_us, max_us, sum_duration_us FROM aggregates"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY bucket DESC"

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	q += fmt.Sprintf(" LIMIT %d", limit)
	if f.Offset > 0 {
		q += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Aggregate
	for rows.Next() {
		var a Aggregate
		if err := rows.Scan(
			&a.ID, &a.ProjectID, &a.Service, &a.Operation, &a.Resource, &a.Kind,
			&a.Bucket, &a.Count, &a.ErrorCount,
			&a.P50Us, &a.P95Us, &a.P99Us, &a.MaxUs, &a.SumDurationUs,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Repository) DeleteAggregatesOlderThan(cutoff time.Time) (int64, error) {
	res, err := r.db.Exec("DELETE FROM aggregates WHERE bucket < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
