package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

// Read results are cached per filter so a reload, a second viewer or a retry
// does not scan again. A fresh entry is served for one grain, a stale one for
// four while one background refresh runs.
//
// A miss computes on a context detached from the request. The request waits
// at most until its own deadline (the API's read budget) and then answers 503;
// the scan runs on to readComputeBudget and fills the cache, so the client's
// retry joins the running scan or reads its result.
const (
	// readComputeBudget is the least time a detached scan gets.
	readComputeBudget = 60 * time.Second
	// readComputeSlots bounds the detached scans running at once, so a viewer
	// clicking through ranges cannot queue unbounded work on the reader.
	readComputeSlots = 4
)

var (
	// readFlight collapses identical concurrent reads into one scan.
	readFlight singleflight.Group
	readSlots  = make(chan struct{}, readComputeSlots)
)

// readDetached returns a context that a cancelled ctx does not cancel. A ctx
// with a deadline keeps it but gets at least readComputeBudget, so a short
// request budget does not cut the scan its retry will read. A ctx without one
// stays without, and the repository caps the scan at its per-query timeout.
func readDetached(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	deadline, ok := ctx.Deadline()
	if !ok {
		return detached, func() {}
	}
	if floor := time.Now().Add(readComputeBudget); deadline.Before(floor) {
		deadline = floor
	}
	return context.WithDeadline(detached, deadline)
}

// readCached returns the cached result for key, computing it once on a miss.
func readCached[T any](ctx context.Context, c *cache.Cache, key string, grain time.Duration, compute func(context.Context) (T, error)) (T, error) {
	if c == nil {
		return compute(ctx)
	}
	if hit, found, fresh := cache.GetSWR[T](c, ctx, key); found {
		if !fresh {
			ch := startRead(ctx, c, key, grain, compute)
			go func() {
				defer observability.RecoverAndReport("read-revalidate", false)
				<-ch
			}()
		}
		return hit, nil
	}
	return awaitRead[T](ctx, startRead(ctx, c, key, grain, compute))
}

// startRead joins or starts the scan for key.
func startRead[T any](ctx context.Context, c *cache.Cache, key string, grain time.Duration, compute func(context.Context) (T, error)) <-chan singleflight.Result {
	return readFlight.DoChan(key, func() (v any, err error) {
		// DoChan re-raises a panic in a fresh goroutine, which would kill the
		// process; report it and fail this read instead.
		defer func() {
			if p := recover(); p != nil {
				observability.ReportPanic("read-compute", p, true)
				err = fmt.Errorf("read %s panicked: %v", key, p)
			}
		}()
		cctx, cancel := readDetached(ctx)
		defer cancel()
		select {
		case readSlots <- struct{}{}:
			defer func() { <-readSlots }()
		case <-cctx.Done():
			return nil, cctx.Err()
		}
		start := time.Now()
		res, err := compute(cctx)
		if err != nil {
			// The request may have left at its budget, so nobody else sees
			// this error: log it, or a scan that never fills the cache is
			// invisible. Info, because a waiting request reports it as well.
			slog.Info("read scan failed", "key", key, "elapsed_ms", time.Since(start).Milliseconds(), "error", err)
			return nil, err
		}
		cache.SetSWR(c, context.Background(), key, res, grain, 4*grain)
		return res, nil
	})
}

// awaitRead waits for the scan until ctx ends.
func awaitRead[T any](ctx context.Context, ch <-chan singleflight.Result) (T, error) {
	var zero T
	select {
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		return r.Val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// readGrain returns the snapping grain for a window of the given width: short
// windows stay close to live, wide ones share an entry for a quarter hour.
func readGrain(window time.Duration) time.Duration { return dashboardGrain(window) }

// snapRange snaps from and to down to the grain of their window, keeping the
// width, so requests inside one grain share one cache entry.
func snapRange(from, to time.Time) (time.Time, time.Time, time.Duration) {
	g := readGrain(to.Sub(from))
	if from.IsZero() || to.IsZero() {
		return from, to, g
	}
	return from.Truncate(g), to.Truncate(g), g
}
