package service

import (
	"context"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

func TestDependenciesAndDatabaseReportTruncatedCoverage(t *testing.T) {
	repo := setupTestRepo(t)
	if err := repo.InsertSpans(dbTestSpans(time.Now())); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	limit := spanReadLimit
	t.Cleanup(func() { spanReadLimit = limit })
	ctx := context.Background()

	for _, tc := range []struct {
		limit     int
		truncated bool
	}{{2, true}, {10, false}} {
		spanReadLimit = tc.limit
		// A fresh service per limit, so the read cache does not answer for the other.
		svc := NewQueryService(repo, nil, nil)
		_, depCov, err := svc.ListDependencies(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil {
			t.Fatalf("ListDependencies: %v", err)
		}
		_, dbCov, err := svc.ListDatabaseQueries(ctx, 1, time.Time{}, time.Time{}, "")
		if err != nil {
			t.Fatalf("ListDatabaseQueries: %v", err)
		}
		for name, c := range map[string]Coverage{"dependencies": depCov, "database": dbCov} {
			if c.Truncated != tc.truncated || (c.CoveredFrom != nil) != tc.truncated {
				t.Errorf("limit %d, %s: Truncated = %v, CoveredFrom = %v; want truncated %v with a start only then",
					tc.limit, name, c.Truncated, c.CoveredFrom, tc.truncated)
			}
		}
	}
}

// The read cache stores values as JSON, so a cache hit must decode to the
// same list and coverage the read returned.
func TestDependenciesAndDatabaseSurviveTheReadCache(t *testing.T) {
	repo := setupTestRepo(t)
	if err := repo.InsertSpans(dbTestSpans(time.Now())); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}
	limit := spanReadLimit
	t.Cleanup(func() { spanReadLimit = limit })
	spanReadLimit = 2
	svc := NewQueryService(repo, nil, nil)
	svc.SetCache(cache.New(cache.NewMemoryStore(), time.Minute))
	ctx := context.Background()

	reads := map[string]func() (int, Coverage, error){
		"dependencies": func() (int, Coverage, error) {
			l, c, err := svc.ListDependencies(ctx, 1, time.Time{}, time.Time{}, "")
			return len(l), c, err
		},
		"database": func() (int, Coverage, error) {
			l, c, err := svc.ListDatabaseQueries(ctx, 1, time.Time{}, time.Time{}, "")
			return len(l), c, err
		},
	}
	for name, read := range reads {
		n, cov, err := read()
		if err != nil || n == 0 || !cov.Truncated {
			t.Fatalf("%s: first read %d rows, coverage %+v, %v", name, n, cov, err)
		}
		hitN, hitCov, err := read()
		if err != nil || hitN != n || hitCov.Truncated != cov.Truncated || hitCov.CoveredFrom == nil || !hitCov.CoveredFrom.Equal(*cov.CoveredFrom) {
			t.Errorf("%s: cache hit %d rows, coverage %+v, %v; want %d rows, coverage %+v", name, hitN, hitCov, err, n, cov)
		}
	}
}
