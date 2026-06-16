package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// MetricRow is the repository-layer representation of a stored metric data point.
type MetricRow struct {
	ID                int64
	ProjectID         int64
	Name              string
	Description       string
	Unit              string
	Type              string
	TimeUnixNano      int64
	StartTimeUnixNano int64
	Value             float64
	Count             int64
	Attributes        string
	Extra             string
	IngestedAt        time.Time
}

// MetricFilter scopes metric queries.
type MetricFilter struct {
	ProjectID  int64
	Name       string
	From       time.Time
	To         time.Time
	Attributes map[string]string // label equality filters via JSON_EXTRACT
	Limit      int
}

// InsertMetrics persists a batch of metric data points in a single transaction.
func (r *Repository) InsertMetrics(ctx context.Context, recs []model.MetricRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO metrics
		(project_id, name, description, unit, type,
		 time_unix_nano, start_time_unix_nano,
		 value, count, attributes, extra)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, rec := range recs {
		attrs := string(rec.Attributes)
		if attrs == "" || attrs == "null" {
			attrs = "{}"
		}
		var extra *string
		if len(rec.Extra) > 0 && string(rec.Extra) != "null" {
			s := string(rec.Extra)
			extra = &s
		}
		if _, err := stmt.ExecContext(ctx,
			rec.ProjectID, rec.Name, rec.Description, rec.Unit, string(rec.Type),
			int64(rec.TimeUnixNano), int64(rec.StartTimeUnixNano),
			rec.Value, int64(rec.Count), attrs, extra,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListMetricNames returns distinct metric names for a project within a time range.
func (r *Repository) ListMetricNames(ctx context.Context, projectID int64, from, to time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT name FROM metrics
		 WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?
		 ORDER BY name LIMIT 1000`,
		projectID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// MetricCatalogEntry summarises one metric name within a time range: its OTLP
// type, unit, and the number of distinct label sets (series) seen.
type MetricCatalogEntry struct {
	Name   string
	Type   string
	Unit   string
	Series int64
}

// ListMetricCatalog returns one entry per metric name for a project within a
// time range, with its type, unit, and distinct-series count.
func (r *Repository) ListMetricCatalog(ctx context.Context, projectID int64, from, to time.Time) ([]MetricCatalogEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	rows, err := r.db.QueryContext(ctx,
		`SELECT name, type, unit, COUNT(DISTINCT attributes) AS series
		 FROM metrics
		 WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?
		 GROUP BY name, type, unit
		 ORDER BY name LIMIT 2000`,
		projectID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricCatalogEntry
	for rows.Next() {
		var e MetricCatalogEntry
		if err := rows.Scan(&e.Name, &e.Type, &e.Unit, &e.Series); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// QueryMetricSeries returns data points for a specific metric name.
func (r *Repository) QueryMetricSeries(ctx context.Context, f MetricFilter) ([]MetricRow, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	where := []string{"project_id = ?", "name = ?", "ingested_at >= ?", "ingested_at <= ?"}
	args := []any{f.ProjectID, f.Name, f.From, f.To}

	for k, v := range f.Attributes {
		// Double-quoting the key handles dots in names like "service.name".
		where = append(where, fmt.Sprintf(`JSON_EXTRACT(attributes, '$."%s"') = ?`, k))
		args = append(args, v)
	}

	limit := f.Limit
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	args = append(args, limit)

	q := fmt.Sprintf(`SELECT id, project_id, name, description, unit, type,
		time_unix_nano, start_time_unix_nano, value, count, attributes, extra, ingested_at
		FROM metrics WHERE %s ORDER BY time_unix_nano ASC LIMIT ?`,
		strings.Join(where, " AND "))

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MetricRow
	for rows.Next() {
		var row MetricRow
		var extra sql.NullString
		if err := rows.Scan(
			&row.ID, &row.ProjectID, &row.Name, &row.Description, &row.Unit, &row.Type,
			&row.TimeUnixNano, &row.StartTimeUnixNano, &row.Value, &row.Count,
			&row.Attributes, &extra, &row.IngestedAt,
		); err != nil {
			return nil, err
		}
		if extra.Valid {
			row.Extra = extra.String
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DeleteMetricsOlderThan removes metric data points ingested before the cutoff,
// in bounded chunks so a large backlog never holds the write lock long enough to
// block ingest (an unbatched DELETE here previously stalled writes for minutes).
func (r *Repository) DeleteMetricsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	var total int64
	for {
		res, err := r.db.ExecContext(ctx,
			`DELETE FROM metrics WHERE rowid IN (SELECT rowid FROM metrics WHERE ingested_at < ? LIMIT 1000)`,
			cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n == 0 {
			break
		}
	}
	return total, nil
}

// MarshalMetricExtra serialises the Extra field of a MetricRow for JSON responses.
func MarshalMetricExtra(row MetricRow) json.RawMessage {
	if row.Extra == "" {
		return nil
	}
	return json.RawMessage(row.Extra)
}
