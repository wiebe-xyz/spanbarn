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

// DBStats holds database file and row count statistics.
type DBStats struct {
	DBSizeBytes    int64 `json:"dbSizeBytes"`
	SpanCount      int64 `json:"spanCount"`
	AggregateCount int64 `json:"aggregateCount"`
	ErrorSampleCount int64 `json:"errorSampleCount"`
	SpoolSizeBytes int64 `json:"spoolSizeBytes"`
}

// GetDBStats returns database size and row count statistics.
func (r *Repository) GetDBStats(dbPath, spoolDir string) (*DBStats, error) {
	stats := &DBStats{}

	if info, err := os.Stat(dbPath); err == nil {
		stats.DBSizeBytes = info.Size()
	}
	walPath := dbPath + "-wal"
	if info, err := os.Stat(walPath); err == nil {
		stats.DBSizeBytes += info.Size()
	}

	if spoolDir != "" {
		entries, err := os.ReadDir(spoolDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					if info, err := e.Info(); err == nil {
						stats.SpoolSizeBytes += info.Size()
					}
				}
			}
		}
	}

	counts := []struct {
		table string
		dest  *int64
	}{
		{"spans", &stats.SpanCount},
		{"aggregates", &stats.AggregateCount},
		{"error_samples", &stats.ErrorSampleCount},
	}
	for _, c := range counts {
		err := r.db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", c.table)).Scan(c.dest)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", c.table, err)
		}
	}

	_ = filepath.Clean(dbPath)
	return stats, nil
}
