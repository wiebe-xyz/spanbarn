package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

func TestReadMetricRangeSharesOneEntryPerWindow(t *testing.T) {
	svc := NewMetricsService(nil)
	svc.SetCache(cache.New(cache.NewMemoryStore(), time.Minute))
	var scans atomic.Int32
	compute := func(_ context.Context, from, to time.Time) (int, error) {
		scans.Add(1)
		return int(to.Sub(from).Hours()), nil
	}

	to := time.Date(2026, 11, 10, 12, 0, 7, 0, time.UTC)
	from := to.Add(-24 * time.Hour)
	first, err := ReadMetricRange(context.Background(), svc, "k", from, to, compute)
	if err != nil {
		t.Fatal(err)
	}
	// A reload a few seconds later lands in the same grain and must not scan again.
	second, err := ReadMetricRange(context.Background(), svc, "k", from.Add(3*time.Second), to.Add(3*time.Second), compute)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || scans.Load() != 1 {
		t.Fatalf("first %d second %d scans %d, want one shared scan", first, second, scans.Load())
	}
	if _, err := ReadMetricRange(context.Background(), svc, "other", from, to, compute); err != nil {
		t.Fatal(err)
	}
	if scans.Load() != 2 {
		t.Fatalf("scans %d, want a different key to scan again", scans.Load())
	}
}

func TestReadMetricRangeWithoutCacheRunsTheRead(t *testing.T) {
	svc := NewMetricsService(nil)
	var scans atomic.Int32
	compute := func(context.Context, time.Time, time.Time) (int, error) { scans.Add(1); return 1, nil }
	now := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := ReadMetricRange(context.Background(), svc, "k", now.Add(-time.Hour), now, compute); err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 2 {
		t.Fatalf("scans %d, want 2 without a cache", scans.Load())
	}
}
