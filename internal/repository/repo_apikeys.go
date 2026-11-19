package repository

import (
	"database/sql"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func (r *Repository) CreateAPIKey(projectID int64, name, keyHash, scope string) (int64, error) {
	var id int64
	err := r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
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

// EnsureAPIKey registers a key by its SHA-256 hash if no key with that hash
// exists yet, and reports whether it inserted one. It is idempotent via the
// UNIQUE index on api_keys(key_hash), so seeding can run on every boot.
//
// Rotating a key seeds a new row rather than replacing the old one: the
// previous key keeps working until it is explicitly revoked, so a rotation
// cannot lock out a client that has not picked up the new value yet.
func (r *Repository) EnsureAPIKey(projectID int64, name, keyHash, scope string) (inserted bool, err error) {
	err = r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		res, e := db.Exec(
			"INSERT OR IGNORE INTO api_keys (project_id, name, key_hash, scope) VALUES (?, ?, ?, ?)",
			projectID, name, keyHash, scope,
		)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		inserted = n > 0
		return e
	})
	return inserted, err
}

func (r *Repository) GetAPIKeyByHash(keyHash string) (APIKey, error) {
	var k APIKey
	err := r.db.QueryRow(
		"SELECT id, project_id, name, key_hash, scope, environment FROM api_keys WHERE key_hash = ?",
		keyHash,
	).Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.Environment)
	return k, err
}

func (r *Repository) TouchAPIKey(id int64) error {
	return r.execLow(engine.FamilyCore, func(db *sql.DB) error {
		_, err := db.Exec("UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?", id)
		return err
	})
}

func (r *Repository) ListAPIKeys(projectID int64) ([]APIKey, error) {
	rows, err := r.db.Query(
		"SELECT id, project_id, name, key_hash, scope, environment, last_used_at, created_at FROM api_keys WHERE project_id = ? ORDER BY id",
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.Environment, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repository) ListAllAPIKeys() ([]APIKey, error) {
	rows, err := r.db.Query(
		"SELECT id, project_id, name, key_hash, scope, environment, last_used_at, created_at FROM api_keys ORDER BY id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Name, &k.KeyHash, &k.Scope, &k.Environment, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey deletes the key and remembers its fingerprint, id, project and
// name in revoked_keys, so a request that still uses it shows up in the
// rejection counters as a revoked key (#572). It returns sql.ErrNoRows for an
// id no key has.
func (r *Repository) RevokeAPIKey(id int64) error {
	return r.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`INSERT OR REPLACE INTO revoked_keys (fingerprint, key_id, project_id, name, revoked_at)
			SELECT substr(key_hash, 1, ?), id, project_id, name, CAST(strftime('%s', 'now') AS INTEGER) FROM api_keys WHERE id = ?`,
			KeyFingerprintLen, id); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM api_keys WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return tx.Commit()
	})
}

// SetAPIKeyEnvironment sets the environment stamped on records that arrive
// without one. An empty value clears it. It reports ErrNoRows-free: a missing
// id changes nothing and returns sql.ErrNoRows.
func (r *Repository) SetAPIKeyEnvironment(id int64, environment string) error {
	return r.execHighExpectingRows(engine.FamilyCore, "UPDATE api_keys SET environment = ? WHERE id = ?", environment, id)
}
