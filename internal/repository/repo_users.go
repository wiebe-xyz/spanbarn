package repository

func (r *Repository) CreateUser(username, passwordHash string) error {
	return r.execHigh(func() error {
		_, err := r.db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, passwordHash)
		return err
	})
}

func (r *Repository) GetUserByUsername(username string) (User, error) {
	var u User
	err := r.db.QueryRow("SELECT id, username, password_hash, e2e_expires_at, created_at FROM users WHERE username = ?", username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.E2EExpiresAt, &u.CreatedAt)
	return u, err
}

func (r *Repository) UpdateUserPassword(username, passwordHash string) error {
	return r.execHighExpectingRows("UPDATE users SET password_hash = ? WHERE username = ?", passwordHash, username)
}

func (r *Repository) DeleteUser(username string) error {
	return r.execHighExpectingRows("DELETE FROM users WHERE username = ?", username)
}

func (r *Repository) ListUsers() ([]User, error) {
	rows, err := r.db.Query("SELECT id, username, password_hash, e2e_expires_at, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.E2EExpiresAt, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
