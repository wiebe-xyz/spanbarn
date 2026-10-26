package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up045, down045)
}

// up045 revokes the setup key of the pending project 'status'. A probe of the
// setup page created that project while the page still answered without a
// session (issue #390). The project stays for its owner to delete; only its
// key goes. On a database without that project it deletes nothing.
func up045(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM api_keys
		WHERE name = 'setup'
		  AND project_id IN (SELECT id FROM projects WHERE slug = 'status' AND status = 'pending')`)
	return err
}

// down045 does not restore the key: it was never meant to exist.
func down045(context.Context, *sql.Tx) error { return nil }
