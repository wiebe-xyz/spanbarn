package ingest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func TestQueueEnqueueDrain(t *testing.T) {
	q := NewQueue(10)

	for i := 0; i < 5; i++ {
		ok := q.Enqueue(model.SpanRecord{SpanID: fmt.Sprintf("span-%d", i)})
		if !ok {
			t.Fatalf("Enqueue(%d) returned false, expected true", i)
		}
	}

	if q.Len() != 5 {
		t.Fatalf("Len() = %d, want 5", q.Len())
	}

	items := q.Drain()
	if len(items) != 5 {
		t.Fatalf("Drain() returned %d items, want 5", len(items))
	}

	for i, item := range items {
		want := fmt.Sprintf("span-%d", i)
		if item.SpanID != want {
			t.Errorf("items[%d].SpanID = %q, want %q", i, item.SpanID, want)
		}
	}

	if q.Len() != 0 {
		t.Fatalf("Len() after drain = %d, want 0", q.Len())
	}
}

func TestQueueFull(t *testing.T) {
	cap := 4
	q := NewQueue(cap)

	for i := 0; i < cap; i++ {
		if !q.Enqueue(model.SpanRecord{SpanID: fmt.Sprintf("s%d", i)}) {
			t.Fatalf("Enqueue(%d) should succeed", i)
		}
	}

	// Queue is full — next enqueue should return false.
	if q.Enqueue(model.SpanRecord{SpanID: "overflow"}) {
		t.Fatal("Enqueue on full queue returned true, expected false")
	}

	if q.Len() != cap {
		t.Fatalf("Len() = %d, want %d", q.Len(), cap)
	}
}

func TestQueueDrainEmpty(t *testing.T) {
	q := NewQueue(8)
	items := q.Drain()
	if items != nil {
		t.Fatalf("Drain() on empty queue = %v, want nil", items)
	}
}

func TestQueueConcurrency(t *testing.T) {
	const (
		goroutines = 16
		perG       = 1000
	)
	q := NewQueue(goroutines * perG)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				q.Enqueue(model.SpanRecord{SpanID: fmt.Sprintf("g%d-s%d", id, i)})
			}
		}(g)
	}
	wg.Wait()

	if q.Len() != goroutines*perG {
		t.Fatalf("Len() = %d, want %d", q.Len(), goroutines*perG)
	}

	items := q.Drain()
	if len(items) != goroutines*perG {
		t.Fatalf("Drain() returned %d items, want %d", len(items), goroutines*perG)
	}
}

func TestQueueWrapAround(t *testing.T) {
	// Verify ring buffer wrapping works correctly.
	q := NewQueue(4)

	// Fill and drain twice to force head to advance.
	for round := 0; round < 3; round++ {
		for i := 0; i < 4; i++ {
			ok := q.Enqueue(model.SpanRecord{SpanID: fmt.Sprintf("r%d-s%d", round, i)})
			if !ok {
				t.Fatalf("round %d: Enqueue(%d) failed", round, i)
			}
		}
		items := q.Drain()
		if len(items) != 4 {
			t.Fatalf("round %d: Drain() = %d items, want 4", round, len(items))
		}
		for i, item := range items {
			want := fmt.Sprintf("r%d-s%d", round, i)
			if item.SpanID != want {
				t.Errorf("round %d: items[%d].SpanID = %q, want %q", round, i, item.SpanID, want)
			}
		}
	}
}
