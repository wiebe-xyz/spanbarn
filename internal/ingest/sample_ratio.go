package ingest

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// SettingsReader fetches a settings value by key.
// Satisfied by *repository.Repository.
type SettingsReader interface {
	GetSetting(key string) (string, error)
}

// CachedRatioLookup reads sample ratios from the settings table and caches
// them for cacheTTL to avoid hammering the DB on every trace decision.
//
// Settings keys (all optional):
//
//	ingest.sample_ratio.default                      → global fallback ratio
//	ingest.sample_ratio.project.{projectID}          → per-project ratio
//	ingest.sample_ratio.project.{projectID}.op.{op}  → per-project + per-operation ratio
//
// A ratio of 1 means keep every trace. 1000 means keep 1 in 1000.
// 0 or negative means use the next level's value.
type CachedRatioLookup struct {
	repo     SettingsReader
	cacheTTL time.Duration

	mu      sync.Mutex
	entries map[string]cachedEntry
}

type cachedEntry struct {
	value     int
	expiresAt time.Time
}

// NewCachedRatioLookup creates a lookup backed by the given settings reader.
func NewCachedRatioLookup(repo SettingsReader, cacheTTL time.Duration) *CachedRatioLookup {
	return &CachedRatioLookup{
		repo:     repo,
		cacheTTL: cacheTTL,
		entries:  make(map[string]cachedEntry),
	}
}

// Ratio returns the most specific configured ratio for (projectID, operation),
// falling back through project-level and global defaults.
func (c *CachedRatioLookup) Ratio(_ context.Context, projectID int64, operation string) int {
	// Most specific: per-project + per-operation.
	opKey := fmt.Sprintf("ingest.sample_ratio.project.%d.op.%s", projectID, operation)
	if v := c.get(opKey); v > 0 {
		return v
	}
	// Per-project default.
	projKey := fmt.Sprintf("ingest.sample_ratio.project.%d", projectID)
	if v := c.get(projKey); v > 0 {
		return v
	}
	// Global default.
	if v := c.get("ingest.sample_ratio.default"); v > 0 {
		return v
	}
	return DefaultSampleRatio
}

func (c *CachedRatioLookup) get(key string) int {
	c.mu.Lock()
	entry, ok := c.entries[key]
	now := time.Now()
	c.mu.Unlock()

	if ok && now.Before(entry.expiresAt) {
		return entry.value
	}

	val := 0
	if s, err := c.repo.GetSetting(key); err == nil && s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			val = n
		}
	}

	c.mu.Lock()
	c.entries[key] = cachedEntry{value: val, expiresAt: now.Add(c.cacheTTL)}
	c.mu.Unlock()
	return val
}

// StaticRatioLookup is a fixed ratio used in tests and standalone mode.
type StaticRatioLookup struct{ ratio int }

func NewStaticRatioLookup(ratio int) *StaticRatioLookup { return &StaticRatioLookup{ratio: ratio} }
func (s *StaticRatioLookup) Ratio(_ context.Context, _ int64, _ string) int { return s.ratio }
