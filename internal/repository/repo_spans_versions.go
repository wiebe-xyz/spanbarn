package repository

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxVersionSpans caps the raw spans one version query reads, so a very busy
// service cannot hold the connection past the query deadline.
const maxVersionSpans = 200000

// VersionStats holds the metrics of one service.version of a service, computed
// from raw spans.
type VersionStats struct {
	Version    string
	Count      int64
	ErrorCount int64
	FirstSeen  time.Time
	LastSeen   time.Time
	P50Us      int64
	P95Us      int64
	P99Us      int64
}

// QueryVersionStatsFromSpans groups a service's raw entry-point spans by the
// service.version attribute. Resource attributes are merged into span
// attributes at ingest, so the value sits in attributes. Spans without a
// version are left out. The scan uses idx_spans_project_ingested.
func (r *Repository) QueryVersionStatsFromSpans(projectID int64, service string, from, to time.Time) ([]VersionStats, error) {
	q, args := versionStatsQuery(projectID, service, from, to)
	ctx, cancel := r.queryContext()
	defer cancel()
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVersionStats(rows)
}

func versionStatsQuery(projectID int64, service string, from, to time.Time) (string, []any) {
	where := []string{"service = ?", "kind = 'server'", `json_extract(attributes, '$."service.version"') IS NOT NULL`}
	args := []any{service}
	if projectID != 0 {
		where = append(where, "project_id = ?")
		args = append(args, projectID)
	}
	if !from.IsZero() {
		where = append(where, "ingested_at >= ?")
		args = append(args, from)
	}
	if !to.IsZero() {
		where = append(where, "ingested_at <= ?")
		args = append(args, to)
	}
	q := fmt.Sprintf(`SELECT json_extract(attributes, '$."service.version"'), duration_us, status, ingested_at
		FROM spans WHERE %s ORDER BY ingested_at DESC LIMIT %d`, strings.Join(where, " AND "), maxVersionSpans)
	return q, args
}

func scanVersionStats(rows *sql.Rows) ([]VersionStats, error) {
	type bucket struct {
		durations   []int64
		errors      int64
		first, last time.Time
	}
	byVersion := make(map[string]*bucket)
	for rows.Next() {
		var version, status string
		var durationUs int64
		var ingestedAt time.Time
		if err := rows.Scan(&version, &durationUs, &status, &ingestedAt); err != nil {
			return nil, err
		}
		if version == "" {
			continue
		}
		b := byVersion[version]
		if b == nil {
			b = &bucket{first: ingestedAt, last: ingestedAt}
			byVersion[version] = b
		}
		b.durations = append(b.durations, durationUs)
		if strings.EqualFold(status, "error") {
			b.errors++
		}
		if ingestedAt.Before(b.first) {
			b.first = ingestedAt
		}
		if ingestedAt.After(b.last) {
			b.last = ingestedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]VersionStats, 0, len(byVersion))
	for version, b := range byVersion {
		sort.Slice(b.durations, func(i, j int) bool { return b.durations[i] < b.durations[j] })
		out = append(out, VersionStats{
			Version:    version,
			Count:      int64(len(b.durations)),
			ErrorCount: b.errors,
			FirstSeen:  b.first,
			LastSeen:   b.last,
			P50Us:      percentileFromSorted(b.durations, 50),
			P95Us:      percentileFromSorted(b.durations, 95),
			P99Us:      percentileFromSorted(b.durations, 99),
		})
	}
	// Newest version first: the one seen most recently, then by first sighting.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.After(out[j].FirstSeen)
		}
		return out[i].Version > out[j].Version
	})
	return out, nil
}
