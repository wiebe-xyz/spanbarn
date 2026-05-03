package repository

import (
	"database/sql"

	"github.com/pressly/goose/v3"

	// Register migrations via init().
	_ "github.com/wiebe-xyz/spanbarn/internal/repository/migrations"
)

// Migrate runs all pending goose migrations against db.
func Migrate(db *sql.DB) error {
	goose.SetBaseFS(nil)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}
