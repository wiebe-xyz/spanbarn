package writescheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type captureHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) find(msg string) (map[string]any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.recs {
		if r.Message != msg {
			continue
		}
		attrs := map[string]any{}
		r.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.Any(); return true })
		return attrs, true
	}
	return nil, false
}

// A job queued behind a long-running one reports how long it waited to start,
// separately from how long it ran.
func TestQueueWaitIsLoggedSeparatelyFromExecution(t *testing.T) {
	s := New()
	s.slowThreshold = 50 * time.Millisecond
	h := &captureHandler{}
	s.SetLogger(slog.New(h))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	started := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Submit(ctx, Low, "blocker", func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Submit(ctx, Low, "fast-but-queued", func() error { return nil })
	}()
	time.Sleep(120 * time.Millisecond)
	close(release)
	<-done
	wg.Wait()

	attrs, ok := h.find("write scheduler: slow queue wait")
	if !ok {
		t.Fatal("expected a slow queue wait log for the queued job")
	}
	if attrs["op"] != "fast-but-queued" {
		t.Fatalf("op = %v", attrs["op"])
	}
	if wait, _ := attrs["wait_ms"].(int64); wait < 100 {
		t.Fatalf("wait_ms = %v, want >= 100", attrs["wait_ms"])
	}
	if _, slow := h.find("write scheduler: slow write"); !slow {
		// the blocker ran >= 120ms, so it is a slow write; its wait is near zero
		t.Fatal("expected the blocker to log a slow write")
	}
}

func TestSlowWriteLogCarriesWaitMs(t *testing.T) {
	s := New()
	s.slowThreshold = 10 * time.Millisecond
	h := &captureHandler{}
	s.SetLogger(slog.New(h))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	_ = s.Submit(ctx, High, "slow", func() error { time.Sleep(30 * time.Millisecond); return nil })

	attrs, ok := h.find("write scheduler: slow write")
	if !ok {
		t.Fatal("expected slow write log")
	}
	if _, has := attrs["wait_ms"]; !has {
		t.Fatal("slow write log has no wait_ms")
	}
}

// Routine slow writes stay at WARN; a write past reportThreshold logs at
// ERROR, the level that reaches BugBarn.
func TestSlowWriteLevel(t *testing.T) {
	s := New()
	h := &captureHandler{}
	s.SetLogger(slog.New(h))
	for _, d := range []time.Duration{time.Second, 3 * time.Second, 12 * time.Second} {
		s.logSlowWrite(d.String(), d, 0)
	}
	got := map[string]slog.Level{}
	for _, r := range h.recs {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "op" {
				got[a.Value.String()] = r.Level
			}
			return true
		})
	}
	want := map[string]slog.Level{"3s": slog.LevelWarn, "12s": slog.LevelError}
	if len(got) != len(want) || got["3s"] != want["3s"] || got["12s"] != want["12s"] {
		t.Errorf("levels = %v, want %v (nothing under 2s)", got, want)
	}
}

// Jobs still queued when Run's context is cancelled run before Run returns,
// and a write submitted after that fails at once instead of waiting forever.
func TestStoppedSchedulerDrainsThenRefuses(t *testing.T) {
	s := New()
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	ran := make(chan string, 3)
	go func() { _ = s.Submit(context.Background(), High, "blocker", func() error { <-release; return nil }) }()
	results := make(chan error, 2)
	for _, label := range []string{"queued-low", "queued-high"} {
		p := Low
		if label == "queued-high" {
			p = High
		}
		go func() { results <- s.Submit(context.Background(), p, label, func() error { ran <- label; return nil }) }()
	}
	// Both jobs wait in the queues before Run starts.
	for len(s.lowPri)+len(s.highPri) < 3 {
		time.Sleep(time.Millisecond)
	}
	runDone := make(chan struct{})
	go func() { s.Run(ctx); close(runDone) }()
	cancel()
	close(release)
	<-runDone
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("queued job: %v", err)
		}
	}
	if len(ran) != 2 {
		t.Errorf("%d queued jobs ran, want 2", len(ran))
	}

	done := make(chan error, 1)
	go func() { done <- s.Submit(context.Background(), Low, "late", func() error { return nil }) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrStopped) {
			t.Errorf("late submit = %v, want ErrStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a submit to a stopped scheduler blocked")
	}
}
