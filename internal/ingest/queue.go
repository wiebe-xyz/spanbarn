package ingest

import (
	"sync"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// DefaultQueueCapacity is the default number of spans the in-memory queue can hold.
const DefaultQueueCapacity = 32768

// Queue is a thread-safe in-memory ring buffer for span records.
// It is designed for the hot path: Enqueue must never block on storage.
type Queue struct {
	mu       sync.Mutex
	buf      []model.SpanRecord
	head     int
	count    int
	capacity int
}

// NewQueue creates a new queue with the given capacity.
func NewQueue(capacity int) *Queue {
	if capacity <= 0 {
		capacity = DefaultQueueCapacity
	}
	return &Queue{
		buf:      make([]model.SpanRecord, capacity),
		capacity: capacity,
	}
}

// Enqueue adds a span record to the queue. Returns false if the queue is full
// (the record is dropped, never blocks).
func (q *Queue) Enqueue(record model.SpanRecord) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.count >= q.capacity {
		return false
	}

	idx := (q.head + q.count) % q.capacity
	q.buf[idx] = record
	q.count++
	return true
}

// Drain atomically removes and returns all items from the queue.
// Returns nil if the queue is empty.
func (q *Queue) Drain() []model.SpanRecord {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.count == 0 {
		return nil
	}

	out := make([]model.SpanRecord, q.count)
	for i := 0; i < q.count; i++ {
		out[i] = q.buf[(q.head+i)%q.capacity]
	}

	q.head = 0
	q.count = 0
	return out
}

// Len returns the current number of items in the queue.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}
