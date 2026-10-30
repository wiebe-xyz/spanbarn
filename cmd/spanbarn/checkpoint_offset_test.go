package main

import (
	"context"
	"testing"
	"time"
)

func TestCheckpointOffsetSpreadsLoopsOverTheInterval(t *testing.T) {
	want := []time.Duration{0, 10 * time.Second, 20 * time.Second}
	for i, w := range want {
		if got := checkpointOffset(i, len(want)); got != w {
			t.Errorf("checkpointOffset(%d, %d) = %v, want %v", i, len(want), got, w)
		}
	}
	if got := checkpointOffset(0, 1); got != 0 {
		t.Errorf("a single loop starts at once, got offset %v", got)
	}
}

func TestWaitOffsetStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if !waitOffset(ctx, 0) {
		t.Error("offset 0 on a live context should run the loop")
	}
	cancel()
	if waitOffset(ctx, 0) {
		t.Error("offset 0 on an ended context should not run the loop")
	}
	if waitOffset(ctx, time.Hour) {
		t.Error("an ended context should end the wait and not run the loop")
	}
}
