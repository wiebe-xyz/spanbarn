package releasemark

import (
	"errors"
	"fmt"
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

	// maxID is the watermark each scan returns; afterIDs records what each
	// scan was asked for.
	maxID     int64
	afterIDs  []int64
	scanErr   error
	createErr error
}

func (f *fakeRepo) RecentSpanVersions(afterID, _ int64) ([]repository.VersionSighting, int64, error) {
	f.afterIDs = append(f.afterIDs, afterID)
	if f.scanErr != nil {
		return nil, afterID, f.scanErr
	}
	return f.sightings, f.maxID, nil
}

func (f *fakeRepo) ReleaseExists(_ int64, v string) (bool, error) { return f.releases[v], nil }

func (f *fakeRepo) CreateRelease(_ int64, v string, _ time.Time) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
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

func TestScanPassesTheWatermarkOn(t *testing.T) {
	f := &fakeRepo{releases: map[string]bool{}, maxID: 40}
	m := newMarker(f)
	m.Scan()
	f.maxID = 55
	m.Scan()

	f.scanErr = errors.New("busy")
	m.Scan()
	f.scanErr = nil
	m.Scan()

	want := []int64{0, 40, 55, 55}
	if fmt.Sprint(f.afterIDs) != fmt.Sprint(want) {
		t.Fatalf("afterIDs = %v, want %v (a failed scan keeps the watermark)", f.afterIDs, want)
	}
}

// The next scan reads only newer spans, so a release that could not be
// created must be retried from the marker's own list.
func TestFailedReleaseIsRetriedOnTheNextScan(t *testing.T) {
	f := &fakeRepo{releases: map[string]bool{}}
	m := newMarker(f)
	m.Scan()

	f.sightings = []repository.VersionSighting{{ProjectID: 1, Version: "3.0.0"}}
	f.createErr = errors.New("busy")
	if n := m.Scan(); n != 0 {
		t.Fatalf("a failed create must not count, got %d", n)
	}
	f.sightings, f.createErr = nil, nil
	if n := m.Scan(); n != 1 {
		t.Fatalf("want the failed release created on the next scan, got %d", n)
	}
	if len(f.created) != 1 || f.created[0] != "3.0.0" {
		t.Fatalf("created = %v, want [3.0.0]", f.created)
	}
}
