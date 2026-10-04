package repository

import (
	"context"
	"fmt"
	"os"
)

// reconcile brings the shards table in line with the files in the shards
// directory, then migrates every shard file: a file without a row (a stop
// between creating the file and recording it) gets a row, and a row without a
// file is removed.
func (m *ShardManager) reconcile(ctx context.Context) error {
	onDisk, err := m.shardFiles()
	if err != nil {
		return err
	}
	rows, err := m.shardRows(ctx)
	if err != nil {
		return err
	}
	for file, key := range onDisk {
		if _, ok := rows[file]; ok {
			continue
		}
		if err := m.record(ctx, m.specs[key.family], key.start, file); err != nil {
			return err
		}
		m.logger.Info("shard file recorded", "file", file)
	}
	for file := range rows {
		if _, ok := onDisk[file]; ok {
			continue
		}
		if _, err := m.main.ExecContext(ctx, `DELETE FROM shards WHERE file = ?`, file); err != nil {
			return fmt.Errorf("forget shard %s: %w", file, err)
		}
		m.logger.Warn("shard row without a file removed", "file", file)
	}
	for file, key := range onDisk {
		h, err := m.openShard(ctx, key.family, file)
		if err != nil {
			return err
		}
		h.Close()
	}
	return nil
}

// shardFiles lists the shard files in the directory of the families this
// manager shards, by file name.
func (m *ShardManager) shardFiles() (map[string]shardKey, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, fmt.Errorf("list shards: %w", err)
	}
	out := map[string]shardKey{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for f, spec := range m.specs {
			if start, ok := spec.parseFile(e.Name()); ok {
				out[e.Name()] = shardKey{f, start}
			}
		}
	}
	return out, nil
}

// shardRows returns the files the shards table lists for the families this
// manager shards.
func (m *ShardManager) shardRows(ctx context.Context) (map[string]bool, error) {
	rows, err := m.main.QueryContext(ctx, `SELECT family, file FROM shards`)
	if err != nil {
		return nil, fmt.Errorf("read shards: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var family, file string
		if err := rows.Scan(&family, &file); err != nil {
			return nil, err
		}
		for f := range m.specs {
			if f.String() == family {
				out[file] = true
			}
		}
	}
	return out, rows.Err()
}
