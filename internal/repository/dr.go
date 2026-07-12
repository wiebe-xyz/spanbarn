package repository

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// settingsTables lists the tables that make up a "settings-only" snapshot:
// configuration an operator needs to keep (projects, users, credentials,
// alert rules, org settings, saved queries, trace exclusions) as opposed to
// high-volume telemetry data (spans, aggregates, metrics, logs, prompt
// records) that's fine to lose in a disaster-recovery reset. Order matters:
// api_keys, alerts, saved_queries, and trace_exclusions all carry a foreign
// key on projects(id), so projects (and users, independently) must be copied
// first. pinned_traces is deliberately excluded — it references trace IDs
// that won't exist after a telemetry reset.
var settingsTables = []string{
	"projects",
	"users",
	"api_keys",
	"alerts",
	"settings",
	"saved_queries",
	"trace_exclusions",
}

// SnapshotSettings builds a fresh, ready-to-serve SQLite database at destPath
// containing only the settings tables copied from srcPath — every telemetry
// table (spans, aggregates, metrics, logs, prompt records, ...) is present
// (via a normal Migrate) but empty. The result can be dropped in directly as
// SPANBARN_DB_PATH for disaster recovery: no further restore or migration
// step is needed.
//
// Returns per-table row counts copied into the snapshot, for the CLI to
// report as a sanity check.
func SnapshotSettings(ctx context.Context, srcPath, destPath string) (map[string]int64, error) {
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove existing snapshot at %s: %w", destPath, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(destPath + suffix)
	}

	src, err := NewReadOnlyDB(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open source database: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping source database: %w", err)
	}

	dest, err := NewDB(destPath)
	if err != nil {
		return nil, fmt.Errorf("create snapshot database: %w", err)
	}
	defer dest.Close()

	if err := Migrate(dest.DB); err != nil {
		return nil, fmt.Errorf("migrate snapshot database: %w", err)
	}

	if _, err := dest.ExecContext(ctx, `ATTACH DATABASE ? AS src`, srcPath); err != nil {
		return nil, fmt.Errorf("attach source database: %w", err)
	}
	defer func() { _, _ = dest.ExecContext(context.Background(), `DETACH DATABASE src`) }()

	counts := make(map[string]int64, len(settingsTables))
	for _, table := range settingsTables {
		if _, err := dest.ExecContext(ctx, fmt.Sprintf(`INSERT INTO main.%s SELECT * FROM src.%s`, table, table)); err != nil {
			return nil, fmt.Errorf("copy table %s: %w", table, err)
		}
		var count int64
		if err := dest.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM main.%s`, table)).Scan(&count); err != nil {
			return nil, fmt.Errorf("count table %s: %w", table, err)
		}
		counts[table] = count
	}

	dest.FinalCheckpoint(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return counts, nil
}
