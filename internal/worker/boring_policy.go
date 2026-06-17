package worker

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// BoringSettingsReader reads settings by key.
// Satisfied by *repository.Repository.
type BoringSettingsReader interface {
	GetSetting(key string) (string, error)
}

// BoringPolicyReader provides per-project boring span policy.
// Satisfied by *CachedBoringPolicy.
type BoringPolicyReader interface {
	// SampleRatio returns 1-in-N sampling for boring spans for the project.
	// 0 = skip all boring spans. 1 = keep all boring spans. N>1 = keep 1-in-N.
	SampleRatio(projectID int64) int
	// VerboseUntil returns the time until which all spans for the project
	// should be stored (verbose / record-in-detail mode). Zero = inactive.
	VerboseUntil(projectID int64) time.Time
	// MinTracesPerMinute returns the minimum number of boring traces that must
	// survive sampling per (project, operation) each minute. Default 1.
	MinTracesPerMinute(projectID int64) int
}

// CachedBoringPolicy reads boring span policy from the settings table with a TTL
// cache to avoid DB hits on every batch.
//
// Settings keys (all optional):
//
//	boring.sample_ratio                  → global 1-in-N (0 or absent = skip all boring spans)
//	boring.sample_ratio.project.{id}     → per-project override
//	boring.verbose_until.project.{id}    → Unix timestamp (seconds) until project is verbose
//	boring.min_traces_per_minute         → global floor: min boring traces kept per (project, op) per minute
//	boring.min_traces_per_minute.project.{id} → per-project override (default 1 when absent)
type CachedBoringPolicy struct {
	repo     BoringSettingsReader
	cacheTTL time.Duration
	mu       sync.Mutex
	entries  map[string]boringEntry
}

type boringEntry struct {
	value     string
	expiresAt time.Time
}

// NewCachedBoringPolicy creates a policy backed by the given settings reader.
func NewCachedBoringPolicy(repo BoringSettingsReader, cacheTTL time.Duration) *CachedBoringPolicy {
	return &CachedBoringPolicy{
		repo:     repo,
		cacheTTL: cacheTTL,
		entries:  make(map[string]boringEntry),
	}
}

func (p *CachedBoringPolicy) SampleRatio(projectID int64) int {
	projKey := fmt.Sprintf("boring.sample_ratio.project.%d", projectID)
	if s := p.get(projKey); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	if s := p.get("boring.sample_ratio"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return 0
}

// DefaultMinTracesPerMinute is the per-(project, operation) survival floor used
// when no setting is configured: at least one boring trace per minute survives
// sampling so quiet, healthy operations never vanish entirely from the UI.
const DefaultMinTracesPerMinute = 1

func (p *CachedBoringPolicy) MinTracesPerMinute(projectID int64) int {
	projKey := fmt.Sprintf("boring.min_traces_per_minute.project.%d", projectID)
	if s := p.get(projKey); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	if s := p.get("boring.min_traces_per_minute"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return n
		}
	}
	return DefaultMinTracesPerMinute
}

func (p *CachedBoringPolicy) VerboseUntil(projectID int64) time.Time {
	key := fmt.Sprintf("boring.verbose_until.project.%d", projectID)
	s := p.get(key)
	if s == "" {
		return time.Time{}
	}
	ts, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func (p *CachedBoringPolicy) get(key string) string {
	p.mu.Lock()
	entry, ok := p.entries[key]
	now := time.Now()
	p.mu.Unlock()

	if ok && now.Before(entry.expiresAt) {
		return entry.value
	}

	val := ""
	if s, err := p.repo.GetSetting(key); err == nil {
		val = s
	}

	p.mu.Lock()
	p.entries[key] = boringEntry{value: val, expiresAt: now.Add(p.cacheTTL)}
	p.mu.Unlock()
	return val
}
