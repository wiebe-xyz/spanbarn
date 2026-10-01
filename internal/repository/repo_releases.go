package repository

import "time"

// Release marks when a version went live. Boards draw it on time series panels.
type Release struct {
	ID         int64     `json:"id"`
	ProjectID  int64     `json:"projectId"`
	Version    string    `json:"version"`
	ReleasedAt time.Time `json:"releasedAt"`
}

func (r *Repository) CreateRelease(projectID int64, version string, at time.Time) (int64, error) {
	var id int64
	err := r.execHigh(func() error {
		res, e := r.db.Exec(
			`INSERT INTO releases (project_id, version, released_at) VALUES (?, ?, ?)`,
			projectID, version, at.UTC(),
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

// ListReleases returns the releases of a project in [from, to], oldest first.
func (r *Repository) ListReleases(projectID int64, from, to time.Time) ([]Release, error) {
	rows, err := r.db.Query(
		`SELECT id, project_id, version, released_at FROM releases
		WHERE project_id = ? AND released_at >= ? AND released_at <= ? ORDER BY released_at, id`,
		projectID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		var rel Release
		if err := rows.Scan(&rel.ID, &rel.ProjectID, &rel.Version, &rel.ReleasedAt); err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}
