package repository

import "database/sql"

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
	err := r.db.QueryRow("SELECT id, slug, name, status, created_at FROM projects WHERE id = ?", id).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt)
	return p, err
}

func (r *Repository) GetProjectBySlug(slug string) (Project, error) {
	var p Project
	err := r.db.QueryRow("SELECT id, slug, name, status, created_at FROM projects WHERE slug = ?", slug).
		Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt)
	return p, err
}

func (r *Repository) ListProjects() ([]Project, error) {
	rows, err := r.db.Query("SELECT id, slug, name, status, created_at FROM projects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *Repository) ListProjectIDs() ([]int64, error) {
	rows, err := r.db.Query("SELECT id FROM projects ORDER BY id")
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

func (r *Repository) EnsureProjectPending(slug, name string) (Project, error) {
	p, err := r.GetProjectBySlug(slug)
	if err == nil {
		return p, nil
	}
	if err != sql.ErrNoRows {
		return Project{}, err
	}
	res, err := r.db.Exec("INSERT INTO projects (slug, name, status) VALUES (?, ?, 'pending')", slug, name)
	if err != nil {
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return r.getProjectByID(id)
}

func (r *Repository) ApproveProject(id int64) (Project, error) {
	res, err := r.db.Exec("UPDATE projects SET status = 'active' WHERE id = ?", id)
	if err != nil {
		return Project{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Project{}, sql.ErrNoRows
	}
	return r.getProjectByID(id)
}

func (r *Repository) EnsureSetupAPIKey(projectID int64, keySHA256 string) error {
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO api_keys (project_id, name, key_hash, scope) VALUES (?, 'setup', ?, 'ingest')`,
		projectID, keySHA256,
	)
	return err
}
