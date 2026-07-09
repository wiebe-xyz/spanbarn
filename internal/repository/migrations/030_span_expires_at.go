package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up030, down030)
}

// up030 adds a nullable expires_at stamp to spans so the boring-span cleanup is a
// trivial indexed range delete instead of a status/duration classification scan.
//
// The staging flush stamps expires_at on the spans it stores: sampled-boring spans
// get ingested_at + the boring retention window (short); interesting spans (kept
// full until the aggregate-then-delete pass) are left NULL. Cleanup then runs
// `DELETE FROM spans WHERE expires_at < now`, seeking the partial index below —
// no scan of the whole table fetching duration_us per row (which had grown to a
// 30s+ write-slot wedge). Pre-existing rows keep expires_at NULL and drain via
// the normal aggregate-then-delete path, so no backfill is needed.
//
// ADD COLUMN with a NULL default is a metadata-only change (no row rewrite), and
// the index is partial (only stamped rows), so both are cheap even mid-flight.
func up030(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE spans ADD COLUMN expires_at DATETIME`,
		`CREATE INDEX IF NOT EXISTS idx_spans_expires ON spans(expires_at) WHERE expires_at IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func down030(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_spans_expires`,
		`ALTER TABLE spans DROP COLUMN expires_at`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
