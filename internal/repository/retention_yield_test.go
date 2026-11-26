package repository

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestYieldAfterBatchWaitsForTheLongerOfYieldAndBatch(t *testing.T) {
	r := &Repository{deleteBatchYield: 20 * time.Millisecond}
	for _, tc := range []struct {
		name string
		ran  time.Duration
		min  time.Duration
	}{
		{"fast batch keeps the fixed pause", 0, 20 * time.Millisecond},
		{"slow batch waits as long as it ran", 60 * time.Millisecond, 60 * time.Millisecond},
	} {
		start := time.Now()
		if err := r.yieldAfterBatch(context.Background(), tc.ran); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := time.Since(start); got < tc.min {
			t.Errorf("%s: waited %v, want at least %v", tc.name, got, tc.min)
		}
	}
}

func TestYieldAfterBatchSkipsWhenYieldIsOff(t *testing.T) {
	r := &Repository{}
	start := time.Now()
	if err := r.yieldAfterBatch(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a zero yield must not wait, whatever the batch took")
	}
}

func TestYieldAfterBatchStopsOnCancel(t *testing.T) {
	r := &Repository{deleteBatchYield: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.yieldAfterBatch(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
