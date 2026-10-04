package main

import (
	"testing"
	"time"
)

func TestTimingsPercentiles(t *testing.T) {
	var ts timings
	for i := 1; i <= 100; i++ {
		ts = append(ts, time.Duration(i)*time.Millisecond)
	}
	if got := ts.pct(0.5); got != 50*time.Millisecond {
		t.Errorf("p50 = %s", got)
	}
	if got := ts.pct(0.95); got != 95*time.Millisecond {
		t.Errorf("p95 = %s", got)
	}
	if got := (timings{}).pct(0.5); got != 0 {
		t.Errorf("empty p50 = %s", got)
	}
}
