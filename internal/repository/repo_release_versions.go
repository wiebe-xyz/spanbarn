package repository

import "time"

// VersionSighting is a service.version seen on entry-point spans of a project,
// with the first time it was seen in the scanned window.
type VersionSighting struct {
	ProjectID int64
	Version   string
	FirstSeen time.Time
}

// RecentSpanVersions lists the distinct service.version values of entry-point
// spans per project, for the spans stored after the span id afterID. It also
// returns the newest span id, which the caller passes as afterID next time, so
// each call reads only the spans stored since the previous one. With afterID 0
// it reads the spans ingested since the given time instead (idx_spans_ingested).
// Spans without a version are left out.
//
// Span ids grow with every insert, including a trace flushed out of staging
// long after its spans were ingested, so the id range misses nothing that an
// ingested_at window would miss.
func (r *Repository) RecentSpanVersions(afterID int64, since time.Time) ([]VersionSighting, int64, error) {
	ctx, cancel := r.queryContext()
	defer cancel()
	var maxID int64
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM spans`).Scan(&maxID); err != nil {
		return nil, afterID, err
	}
	if maxID <= afterID {
		return nil, afterID, nil
	}
	window, arg := `id > ?`, any(afterID)
	if afterID == 0 {
		window, arg = `ingested_at >= ?`, since.UTC()
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, json_extract(attributes, '$."service.version"') AS version, MIN(ingested_at)
		FROM spans
		WHERE `+window+` AND id <= ? AND kind = 'server' AND json_extract(attributes, '$."service.version"') IS NOT NULL
		GROUP BY project_id, version`, arg, maxID)
	if err != nil {
		return nil, afterID, err
	}
	defer rows.Close()
	var out []VersionSighting
	for rows.Next() {
		var s VersionSighting
		var first any
		if err := rows.Scan(&s.ProjectID, &s.Version, &first); err != nil {
			return nil, afterID, err
		}
		s.FirstSeen = asTime(first)
		if s.Version != "" {
			out = append(out, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, afterID, err
	}
	return out, maxID, nil
}

// ReleaseExists reports whether the project already has a release with this version.
func (r *Repository) ReleaseExists(projectID int64, version string) (bool, error) {
	var n int
	err := r.db.QueryRow(`SELECT COUNT(1) FROM releases WHERE project_id = ? AND version = ?`, projectID, version).Scan(&n)
	return n > 0, err
}

// asTime reads a MIN(datetime) result, which the driver returns as a string or
// time depending on the column affinity of the aggregate.
func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05.999999999-07:00", "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed
			}
		}
	}
	return time.Now().UTC()
}
