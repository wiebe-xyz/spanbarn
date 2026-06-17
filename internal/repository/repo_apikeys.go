package repository

import "database/sql"

func (r *Repository) CreateAPIKey(projectID int64, name, keyHash, scope string) (int64, error) {
	var id int64
	err := r.execHigh(func() error {
		res, e := r.db.Exec(
			"INSERT INTO api_keys (project_id, name, key_hash, scope) VALUES (?, ?, ?, ?)",
			projectID, name, keyHash, scope,
		)
		if e != nil {
			return e
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

func (r *Repository) GetAPIKeyByHash(keyHash string) (APIKey, error) {
	var k APIKey
	err := r.db.QueryRow(
		"SELECT id, project_id, name, key_hash, scope FROM api_keys WHERE key_hash = ?",
		keyHash,
	).Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope)
	return k, err
}

func (r *Repository) TouchAPIKey(id int64) error {
	return r.execLow(func() error {
		_, err := r.db.Exec("UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", id)
		return err
	})
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
	return r.execHigh(func() error {
		res, err := r.db.Exec("DELETE FROM api_keys WHERE id = ?", id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}
