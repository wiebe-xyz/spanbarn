package repository

import (
	"database/sql"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// KeyRejectionRetention is how long a rejection row is kept after its newest
// request. The ask was at least 7 days.
const KeyRejectionRetention = 30 * 24 * time.Hour

// KeyFingerprintLen is the number of hex digits of a key hash that name the key
// in the rejection counters. It equals auth.KeyFingerprintLen; the repository
// does not import auth, and a test holds the two together.
const KeyFingerprintLen = 8

// KeyRejection is one refused key as the API reports it. Revoked is set when the
// fingerprint belongs to a key revoked since the counters exist; the key's id,
// project and name come from revoked_keys.
type KeyRejection struct {
	Fingerprint string
	Count       int64
	FirstSeen   time.Time
	LastSeen    time.Time
	Revoked     bool
	KeyID       int64
	ProjectID   int64
	KeyName     string
	RevokedAt   time.Time
}

// AddKeyRejections adds the counts to the fingerprints' rows in one
// transaction and drops rows whose newest request is older than the retention.
func (r *Repository) AddKeyRejections(rows []model.KeyRejection, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	return r.execLow(engine.FamilyCore, func(db *sql.DB) error {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		for _, row := range rows {
			seen := row.LastSeen
			if seen == 0 {
				seen = now.Unix()
			}
			if _, err := tx.Exec(`INSERT INTO key_rejections (fingerprint, count, first_seen, last_seen) VALUES (?, ?, ?, ?)
				ON CONFLICT(fingerprint) DO UPDATE SET count = count + excluded.count, last_seen = MAX(last_seen, excluded.last_seen)`,
				row.Fingerprint, row.Count, seen, seen); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM key_rejections WHERE last_seen < ?`, now.Add(-KeyRejectionRetention).Unix()); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ListKeyRejections returns the fingerprints refused since the given time,
// newest first, each with the revoked key it matches when there is one.
func (r *Repository) ListKeyRejections(since time.Time) ([]KeyRejection, error) {
	rows, err := r.db.Query(`SELECT k.fingerprint, k.count, k.first_seen, k.last_seen,
			v.fingerprint IS NOT NULL, COALESCE(v.key_id, 0), COALESCE(v.project_id, 0), COALESCE(v.name, ''), COALESCE(v.revoked_at, 0)
		FROM key_rejections k LEFT JOIN revoked_keys v ON v.fingerprint = k.fingerprint
		WHERE k.last_seen >= ? ORDER BY k.last_seen DESC, k.fingerprint`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyRejection
	for rows.Next() {
		var k KeyRejection
		var first, last, revokedAt int64
		if err := rows.Scan(&k.Fingerprint, &k.Count, &first, &last, &k.Revoked, &k.KeyID, &k.ProjectID, &k.KeyName, &revokedAt); err != nil {
			return nil, err
		}
		k.FirstSeen, k.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		if revokedAt > 0 {
			k.RevokedAt = time.Unix(revokedAt, 0).UTC()
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
