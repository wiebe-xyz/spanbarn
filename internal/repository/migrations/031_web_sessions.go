package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up031, down031)
}

// up031 creates web_sessions: token-bound server-side browser sessions. The
// `session` cookie holds an opaque random handle; id_hash is its SHA-256 hex,
// so a leaked DB/backup contains no usable session credentials. OIDC sessions
// store the IamBarn token set server-side (the raw-token browser cookies are
// gone); local-password and e2e sessions get rows too (token columns NULL) so
// one middleware and one revocation story cover every auth method.
//
// Timestamps are unix seconds (INTEGER): rows are matched/pruned by numeric
// comparison only, never rendered from SQL.
func up031(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS web_sessions (
			id_hash               TEXT PRIMARY KEY,
			username              TEXT NOT NULL,
			auth_method           TEXT NOT NULL CHECK (auth_method IN ('oidc','local','e2e')),
			idp_sub               TEXT,
			idp_sid               TEXT,
			id_token              TEXT,
			access_token          TEXT,
			refresh_token         TEXT,
			access_expires_at     INTEGER,
			claims_json           TEXT,
			created_at            INTEGER NOT NULL,
			absolute_expires_at   INTEGER NOT NULL,
			last_refresh_at       INTEGER,
			refresh_failing_since INTEGER
		)`,
		// Back-channel logout deletes by IdP session id (sid) or, when the
		// logout token carries only a subject, by sub.
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_idp_sid ON web_sessions(idp_sid) WHERE idp_sid IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_idp_sub ON web_sessions(idp_sub) WHERE idp_sub IS NOT NULL`,
		// Retention prunes by the absolute cap.
		`CREATE INDEX IF NOT EXISTS idx_web_sessions_absolute_expires ON web_sessions(absolute_expires_at)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down031(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_web_sessions_absolute_expires`,
		`DROP INDEX IF EXISTS idx_web_sessions_idp_sub`,
		`DROP INDEX IF EXISTS idx_web_sessions_idp_sid`,
		`DROP TABLE IF EXISTS web_sessions`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
