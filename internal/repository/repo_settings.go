package repository

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// GetSetting returns a setting value by key, or empty string if not found.
func (r *Repository) GetSetting(key string) (string, error) {
	var val string
	err := r.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSetting upserts a setting key/value pair.
func (r *Repository) SetSetting(key, value string) error {
	_, err := r.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

// DeleteSetting removes a setting key. No-op if the key does not exist.
func (r *Repository) DeleteSetting(key string) error {
	_, err := r.db.Exec("DELETE FROM settings WHERE key = ?", key)
	return err
}

// GetAllSettings returns all settings as a map.
func (r *Repository) GetAllSettings() (map[string]string, error) {
	rows, err := r.db.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}

// DBSize holds filesystem size stats for the database and spool. Cheap to compute.
type DBSize struct {
	DBSizeBytes    int64 `json:"dbSizeBytes"`
	SpoolSizeBytes int64 `json:"spoolSizeBytes"`
}

// DBCounts holds row-count statistics. Expensive — each COUNT(*) is a full
// table scan on SQLite, so callers should cache aggressively.
type DBCounts struct {
	SpanCount        int64 `json:"spanCount"`
	AggregateCount   int64 `json:"aggregateCount"`
	ErrorSampleCount int64 `json:"errorSampleCount"`
}

// GetDBSize returns filesystem sizes for the SQLite db (+WAL) and the spool dir.
func (r *Repository) GetDBSize(dbPath, spoolDir string) (*DBSize, error) {
	size := &DBSize{}

	if info, err := os.Stat(dbPath); err == nil {
		size.DBSizeBytes = info.Size()
	}
	walPath := dbPath + "-wal"
	if info, err := os.Stat(walPath); err == nil {
		size.DBSizeBytes += info.Size()
	}

	if spoolDir != "" {
		entries, err := os.ReadDir(spoolDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					if info, err := e.Info(); err == nil {
						size.SpoolSizeBytes += info.Size()
					}
				}
			}
		}
	}

	_ = filepath.Clean(dbPath)
	return size, nil
}

// GetDBCounts returns total row counts for spans, aggregates, and error_samples.
func (r *Repository) GetDBCounts() (*DBCounts, error) {
	counts := &DBCounts{}
	tables := []struct {
		table string
		dest  *int64
	}{
		{"spans", &counts.SpanCount},
		{"aggregates", &counts.AggregateCount},
		{"error_samples", &counts.ErrorSampleCount},
	}
	for _, c := range tables {
		if err := r.db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", c.table)).Scan(c.dest); err != nil {
			return nil, fmt.Errorf("count %s: %w", c.table, err)
		}
	}
	return counts, nil
}
