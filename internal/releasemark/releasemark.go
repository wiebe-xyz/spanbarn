// Package releasemark records a release marker the first time a service.version
// shows up in a project, so the release lines on board panels do not depend on
// someone posting each version by hand.
package releasemark

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const (
	// DefaultInterval is how often the recent spans are scanned.
	DefaultInterval = time.Minute
	// seedSpans is how many of the newest spans the first scan after start
	// reads, about 20 minutes of prod traffic. That scan only records the
	// versions already running. Later scans read only the spans stored since
	// the previous scan.
	seedSpans = 20000
)

// Repository is the storage the marker needs.
type Repository interface {
	RecentSpanVersions(afterID, seed int64) ([]repository.VersionSighting, int64, error)
	ReleaseExists(projectID int64, version string) (bool, error)
	CreateRelease(projectID int64, version string, at time.Time) (int64, error)
}

// Marker scans recent spans for service.version values that have no release yet.
type Marker struct {
	repo     Repository
	logger   *slog.Logger
	interval time.Duration

	mu    sync.Mutex
	known map[key]bool
	// seeded is false until the first scan, which only records the versions
	// already running. Marking them would date a long-running version to the
	// moment this process started.
	seeded bool
	// lastID is the newest span id the previous scan covered, 0 before the
	// first successful scan. A failed scan leaves it, so the next one rereads.
	lastID int64
	// retry holds sightings whose release could not be checked or created.
	// The next scan reads only newer spans, so it retries these itself.
	retry []repository.VersionSighting
}

type key struct {
	project int64
	version string
}

// New returns a Marker that scans every interval (DefaultInterval when zero).
func New(repo Repository, interval time.Duration, logger *slog.Logger) *Marker {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Marker{repo: repo, logger: logger, interval: interval, known: map[key]bool{}}
}

// Run scans until ctx is canceled.
func (m *Marker) Run(ctx context.Context) {
	m.Scan()
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Scan()
		}
	}
}

// Scan runs one pass and returns how many releases it created.
func (m *Marker) Scan() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	sightings, lastID, err := m.repo.RecentSpanVersions(m.lastID, seedSpans)
	if err != nil {
		m.logger.Warn("release marks: cannot scan recent spans", "error", err)
		return 0
	}
	pending := append(m.retry, sightings...)
	m.retry = nil
	created := 0
	for _, s := range pending {
		if m.mark(s) {
			created++
		}
	}
	m.seeded = true
	m.lastID = lastID
	return created
}

// mark creates the release for one sighting and reports whether it did.
func (m *Marker) mark(s repository.VersionSighting) bool {
	k := key{s.ProjectID, s.Version}
	if m.known[k] {
		return false
	}
	m.known[k] = true
	if !m.seeded {
		return false
	}
	exists, err := m.repo.ReleaseExists(s.ProjectID, s.Version)
	if err != nil {
		m.logger.Warn("release marks: cannot check for a release", "project_id", s.ProjectID, "version", s.Version, "error", err)
		delete(m.known, k)
		m.retry = append(m.retry, s)
		return false
	}
	if exists {
		return false
	}
	if _, err := m.repo.CreateRelease(s.ProjectID, s.Version, s.FirstSeen); err != nil {
		m.logger.Warn("release marks: cannot create a release", "project_id", s.ProjectID, "version", s.Version, "error", err)
		delete(m.known, k)
		m.retry = append(m.retry, s)
		return false
	}
	m.logger.Info("release marks: new version", "project_id", s.ProjectID, "version", s.Version)
	return true
}
