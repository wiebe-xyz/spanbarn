package releasemark

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

type fakeRepo struct {
	sightings []repository.VersionSighting
	releases  map[string]bool
	created   []string
}

func (f *fakeRepo) RecentSpanVersions(time.Time) ([]repository.VersionSighting, error) {
	return f.sightings, nil
}

func (f *fakeRepo) ReleaseExists(_ int64, v string) (bool, error) { return f.releases[v], nil }

func (f *fakeRepo) CreateRelease(_ int64, v string, _ time.Time) (int64, error) {
	f.created = append(f.created, v)
	f.releases[v] = true
	return 1, nil
}

func newMarker(f *fakeRepo) *Marker {
	return New(f, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestFirstScanOnlySeeds(t *testing.T) {
	f := &fakeRepo{releases: map[string]bool{}, sightings: []repository.VersionSighting{{ProjectID: 1, Version: "1.0.0"}}}
	m := newMarker(f)
	if n := m.Scan(); n != 0 {
		t.Fatalf("the first scan must only record running versions, created %d", n)
	}
}

func TestNewVersionAfterSeedIsMarkedOnce(t *testing.T) {
	f := &fakeRepo{releases: map[string]bool{}, sightings: []repository.VersionSighting{{ProjectID: 1, Version: "1.0.0"}}}
	m := newMarker(f)
	m.Scan()

	f.sightings = append(f.sightings, repository.VersionSighting{ProjectID: 1, Version: "1.1.0", FirstSeen: time.Now()})
	if n := m.Scan(); n != 1 {
		t.Fatalf("want 1 release for the new version, got %d", n)
	}
	if n := m.Scan(); n != 0 {
		t.Fatalf("a version is marked once, got %d on the next scan", n)
	}
	if len(f.created) != 1 || f.created[0] != "1.1.0" {
		t.Fatalf("created = %v, want [1.1.0]", f.created)
	}
}

func TestExistingReleaseIsNotDuplicated(t *testing.T) {
	f := &fakeRepo{releases: map[string]bool{"2.0.0": true}}
	m := newMarker(f)
	m.Scan()
	f.sightings = []repository.VersionSighting{{ProjectID: 1, Version: "2.0.0"}}
	if n := m.Scan(); n != 0 {
		t.Fatalf("a version with a posted release must not be marked again, got %d", n)
	}
}
