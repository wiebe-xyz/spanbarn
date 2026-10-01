package migrations

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(up036, down036)
}

// up036 adds saved_queries.filters, the JSON form of the shared filter model
// (internal/filter), and maps the four fixed fields of existing rows onto it:
//
//	service         -> {"key":"service","op":"=","value":...}
//	operation       -> {"key":"name","op":"=","value":...}
//	status          -> {"key":"status","op":"=","value":...}
//	min_duration_us -> {"key":"duration_us","op":">=","value":...}
//
// The old columns stay. Rows written by a build that predates this migration
// keep working through them, and a rollback reads them. The API fills filters
// from the four fields when a client still sends only those.
//
// saved_queries holds a handful of rows per project, so the backfill reads and
// updates them one by one. The JSON shape is written out here instead of
// importing internal/filter, so the migration keeps its meaning if that package
// changes.
func up036(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE saved_queries ADD COLUMN filters TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, service, operation, status, min_duration_us FROM saved_queries`)
	if err != nil {
		return err
	}
	type legacy struct {
		id            int64
		service, op   string
		status        string
		minDurationUs int64
	}
	var all []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.service, &l.op, &l.status, &l.minDurationUs); err != nil {
			rows.Close()
			return err
		}
		all = append(all, l)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, l := range all {
		doc := legacyFilterJSON(l.service, l.op, l.status, l.minDurationUs)
		if doc == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE saved_queries SET filters = ? WHERE id = ?`, doc, l.id); err != nil {
			return err
		}
	}
	return nil
}

type legacyCond struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

func legacyFilterJSON(service, operation, status string, minDurationUs int64) string {
	var conds []legacyCond
	if service != "" {
		conds = append(conds, legacyCond{"service", "=", service})
	}
	if operation != "" {
		conds = append(conds, legacyCond{"name", "=", operation})
	}
	if status != "" {
		conds = append(conds, legacyCond{"status", "=", status})
	}
	if minDurationUs > 0 {
		conds = append(conds, legacyCond{"duration_us", ">=", itoa(minDurationUs)})
	}
	if len(conds) == 0 {
		return ""
	}
	b, err := json.Marshal(struct {
		Match   string       `json:"match"`
		Filters []legacyCond `json:"filters"`
	}{"and", conds})
	if err != nil {
		return ""
	}
	return string(b)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func down036(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE saved_queries DROP COLUMN filters`)
	return err
}
