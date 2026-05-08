package livetail

import (
	"sync"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

type Subscriber struct {
	C      chan model.SpanRecord
	done   chan struct{}
	closed bool
	mu     sync.Mutex
}

func (s *Subscriber) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
}

type Broadcaster struct {
	mu   sync.RWMutex
	subs map[*Subscriber]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[*Subscriber]struct{})}
}

func (b *Broadcaster) Subscribe(bufSize int) *Subscriber {
	if bufSize <= 0 {
		bufSize = 100
	}
	s := &Subscriber{
		C:    make(chan model.SpanRecord, bufSize),
		done: make(chan struct{}),
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Broadcaster) Unsubscribe(s *Subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
	s.Close()
}

func (b *Broadcaster) Publish(record model.SpanRecord) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.C <- record:
		default:
			// drop if subscriber is slow
		}
	}
}

func (b *Broadcaster) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
