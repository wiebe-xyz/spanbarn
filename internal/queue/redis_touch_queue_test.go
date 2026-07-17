package queue

import (
	"testing"
	"time"
)

// Authorize calls TouchAPIKey on every authenticated request, so the coalescing
// window is what keeps this off the hot path. Without it a busy key would LPUSH
// once per request.
func TestTouchPublisherCoalescesPerKey(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	p := NewTouchPublisher(nil, time.Minute, nil)
	p.now = func() time.Time { return now }

	if !p.claim(1) {
		t.Fatal("first claim = false, want true")
	}
	for i := 0; i < 100; i++ {
		if p.claim(1) {
			t.Fatal("claim within the interval = true, want false")
		}
	}

	// A different key is tracked independently.
	if !p.claim(2) {
		t.Error("claim for a new key = false, want true")
	}

	// Once the interval elapses the key is due again.
	now = now.Add(time.Minute)
	if !p.claim(1) {
		t.Error("claim after the interval = false, want true")
	}
}

func TestNewTouchPublisherDefaultsInterval(t *testing.T) {
	if got := NewTouchPublisher(nil, 0, nil).interval; got != DefaultTouchInterval {
		t.Errorf("interval = %v, want %v", got, DefaultTouchInterval)
	}
	if got := NewTouchPublisher(nil, -1, nil).interval; got != DefaultTouchInterval {
		t.Errorf("interval = %v, want %v", got, DefaultTouchInterval)
	}
}
