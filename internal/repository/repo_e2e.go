package repository

import "time"

const E2EAccountTTL = 7 * 24 * time.Hour

// UpsertE2EUser creates or refreshes a user marked as an E2E test account.
// The password hash is set to an empty string — E2E users cannot log in via
// the normal password form; they are only accessible through the E2E session
// endpoint while e2e_enabled is true on the associated project.
func (r *Repository) UpsertE2EUser(username string, expiresAt time.Time) (User, error) {
	_, err := r.db.Exec(`
		INSERT INTO users (username, password_hash, e2e_expires_at)
		VALUES (?, '', ?)
		ON CONFLICT(username) DO UPDATE SET e2e_expires_at = excluded.e2e_expires_at
	`, username, expiresAt)
	if err != nil {
		return User{}, err
	}
	return r.GetUserByUsername(username)
}

// DeleteExpiredE2EUsers removes all users whose e2e_expires_at is in the past.
// Returns the number of rows deleted.
func (r *Repository) DeleteExpiredE2EUsers(now time.Time) (int64, error) {
	res, err := r.db.Exec(
		"DELETE FROM users WHERE e2e_expires_at IS NOT NULL AND e2e_expires_at < ?",
		now,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
