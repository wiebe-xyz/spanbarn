package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

// A request whose budget ends first gets its deadline error, while the scan
// runs on and fills the cache, so the retry is served without scanning again.
func TestReadCachedOutlivesTheRequestBudget(t *testing.T) {
	c := cache.New(cache.NewMemoryStore(), time.Minute)
	release := make(chan struct{})
	var scans atomic.Int32
	compute := func(ctx context.Context) ([]int, error) {
		scans.Add(1)
		<-release
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("the detached scan lost its deadline")
		}
		return []int{1, 2}, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := readCached(ctx, c, "budget-test", time.Minute, compute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first request: err = %v, want its deadline", err)
	}
	close(release)

	retry, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	got, err := readCached(retry, c, "budget-test", time.Minute, compute)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("retry = %v, want the scan's result", got)
	}
	if n := scans.Load(); n != 1 {
		t.Errorf("scans = %d, want 1 (the retry joins or reads the first)", n)
	}
}

// The detached scan gets at least the compute budget, and keeps a longer
// deadline such as the warmer's.
func TestReadDetachedBudget(t *testing.T) {
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	d, cancelD := readDetached(short)
	defer cancelD()
	if dl, _ := d.Deadline(); time.Until(dl) < readComputeBudget-time.Second {
		t.Errorf("short budget: detached deadline in %s, want about %s", time.Until(dl), readComputeBudget)
	}

	long, cancelLong := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelLong()
	d, cancelD = readDetached(long)
	defer cancelD()
	if dl, _ := d.Deadline(); time.Until(dl) < 4*time.Minute {
		t.Errorf("long budget: detached deadline in %s, want the caller's 5m", time.Until(dl))
	}

	d, cancelD = readDetached(context.Background())
	defer cancelD()
	if _, ok := d.Deadline(); ok {
		t.Error("no deadline: the detached scan must leave the cap to the repository")
	}
}

// A panicking scan fails its read instead of killing the process.
func TestReadCachedPanicFailsTheRead(t *testing.T) {
	c := cache.New(cache.NewMemoryStore(), time.Minute)
	_, err := readCached(context.Background(), c, "panic-test", time.Minute, func(context.Context) (*int, error) {
		panic("boom")
	})
	if err == nil {
		t.Fatal("err = nil, want the panic as an error")
	}
}
