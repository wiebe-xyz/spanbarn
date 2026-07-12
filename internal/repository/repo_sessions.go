package repository

import (
	"database/sql"
	"time"
)

// WebSession is one token-bound server-side browser session. The `session`
// cookie holds an opaque handle; IDHash is its SHA-256 hex and the row key.
// OIDC sessions carry the IamBarn token set; local/e2e sessions leave the
// token fields empty (stored as NULL).
type WebSession struct {
	IDHash              string
	Username            string
	AuthMethod          string // "oidc", "local" or "e2e"
	IdpSub              string
	IdpSid              string
	IDToken             string
	AccessToken         string
	RefreshToken        string
	AccessExpiresAt     int64 // unix seconds; 0 = not applicable
	ClaimsJSON          string
	CreatedAt           int64 // unix seconds
	AbsoluteExpiresAt   int64 // unix seconds; hard cap regardless of refreshes
	LastRefreshAt       int64 // unix seconds; 0 = never refreshed
	RefreshFailingSince int64 // unix seconds; 0 = refresh not failing
}

// CreateWebSession inserts a session row. IDHash must be unique (it derives
// from a fresh random token, so collisions do not occur in practice).
func (r *Repository) CreateWebSession(ws WebSession) error {
	return r.execHigh(func() error {
		_, err := r.db.Exec(`
			INSERT INTO web_sessions (
				id_hash, username, auth_method, idp_sub, idp_sid,
				id_token, access_token, refresh_token, access_expires_at,
				claims_json, created_at, absolute_expires_at,
				last_refresh_at, refresh_failing_since
			) VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''),
				NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, 0),
				NULLIF(?, ''), ?, ?, NULLIF(?, 0), NULLIF(?, 0))`,
			ws.IDHash, ws.Username, ws.AuthMethod, ws.IdpSub, ws.IdpSid,
			ws.IDToken, ws.AccessToken, ws.RefreshToken, ws.AccessExpiresAt,
			ws.ClaimsJSON, ws.CreatedAt, ws.AbsoluteExpiresAt,
			ws.LastRefreshAt, ws.RefreshFailingSince)
		return err
	})
}

// GetWebSessionByIDHash loads a session row. Returns sql.ErrNoRows when the
// handle is unknown (revoked, expired-and-pruned, or forged).
func (r *Repository) GetWebSessionByIDHash(idHash string) (WebSession, error) {
	var ws WebSession
	var idpSub, idpSid, idToken, accessToken, refreshToken, claimsJSON sql.NullString
	var accessExpiresAt, lastRefreshAt, refreshFailingSince sql.NullInt64
	err := r.db.QueryRow(`
		SELECT id_hash, username, auth_method, idp_sub, idp_sid,
			id_token, access_token, refresh_token, access_expires_at,
			claims_json, created_at, absolute_expires_at,
			last_refresh_at, refresh_failing_since
		FROM web_sessions WHERE id_hash = ?`, idHash).Scan(
		&ws.IDHash, &ws.Username, &ws.AuthMethod, &idpSub, &idpSid,
		&idToken, &accessToken, &refreshToken, &accessExpiresAt,
		&claimsJSON, &ws.CreatedAt, &ws.AbsoluteExpiresAt,
		&lastRefreshAt, &refreshFailingSince)
	if err != nil {
		return WebSession{}, err
	}
	ws.IdpSub = idpSub.String
	ws.IdpSid = idpSid.String
	ws.IDToken = idToken.String
	ws.AccessToken = accessToken.String
	ws.RefreshToken = refreshToken.String
	ws.AccessExpiresAt = accessExpiresAt.Int64
	ws.ClaimsJSON = claimsJSON.String
	ws.LastRefreshAt = lastRefreshAt.Int64
	ws.RefreshFailingSince = refreshFailingSince.Int64
	return ws, nil
}

// UpdateWebSessionTokens stores the rotated token set after a successful
// refresh, stamps last_refresh_at, and clears refresh_failing_since. An empty
// idToken/claimsJSON keeps the previous value (a refresh response without an
// id_token must not wipe the claims snapshot).
func (r *Repository) UpdateWebSessionTokens(idHash, idToken, accessToken, refreshToken string, accessExpiresAt int64, claimsJSON string, lastRefreshAt int64) error {
	return r.execHighExpectingRows(`
		UPDATE web_sessions SET
			id_token = COALESCE(NULLIF(?, ''), id_token),
			access_token = ?,
			refresh_token = ?,
			access_expires_at = ?,
			claims_json = COALESCE(NULLIF(?, ''), claims_json),
			last_refresh_at = ?,
			refresh_failing_since = NULL
		WHERE id_hash = ?`,
		idToken, accessToken, refreshToken, accessExpiresAt, claimsJSON, lastRefreshAt, idHash)
}

// MarkWebSessionRefreshFailing records the start of a transient refresh
// outage. It only sets the stamp when none is present, so the grace window is
// measured from the FIRST failure, not the latest retry.
func (r *Repository) MarkWebSessionRefreshFailing(idHash string, since int64) error {
	return r.execHigh(func() error {
		_, err := r.db.Exec(`
			UPDATE web_sessions SET refresh_failing_since = ?
			WHERE id_hash = ? AND refresh_failing_since IS NULL`, since, idHash)
		return err
	})
}

// DeleteWebSession removes one session row (logout, invalid_grant, absolute
// expiry). Deleting an already-gone row is not an error.
func (r *Repository) DeleteWebSession(idHash string) error {
	return r.execHigh(func() error {
		_, err := r.db.Exec(`DELETE FROM web_sessions WHERE id_hash = ?`, idHash)
		return err
	})
}

// DeleteWebSessionsByIdpSid removes every session bound to one IdP session id
// (back-channel logout with sid). Returns the number of rows deleted.
func (r *Repository) DeleteWebSessionsByIdpSid(sid string) (int64, error) {
	var n int64
	err := r.execHigh(func() error {
		res, e := r.db.Exec(`DELETE FROM web_sessions WHERE idp_sid = ?`, sid)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// DeleteWebSessionsByIdpSub removes every session of one IdP subject
// (back-channel logout without sid, user suspension). Returns rows deleted.
func (r *Repository) DeleteWebSessionsByIdpSub(sub string) (int64, error) {
	var n int64
	err := r.execHigh(func() error {
		res, e := r.db.Exec(`DELETE FROM web_sessions WHERE idp_sub = ?`, sub)
		if e != nil {
			return e
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

// DeleteExpiredWebSessions prunes rows past their absolute cap. Runs in the
// retention worker's background loop; expired rows are already unusable (the
// middleware enforces absolute_expires_at at request time), this just keeps
// the table from accumulating.
func (r *Repository) DeleteExpiredWebSessions(now time.Time) (int64, error) {
	return r.execLowAffecting(
		`DELETE FROM web_sessions WHERE absolute_expires_at < ?`, now.Unix())
}
