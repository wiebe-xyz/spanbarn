package ingest

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxPerMinute is the per-(project, operation) rate above which
	// non-error spans are dropped at ingest. Health checks and other
	// high-frequency boring operations typically exceed this quickly.
	DefaultMaxPerMinute = 60

	// errorTraceTTL is how long a trace is kept in the error allowlist after
	// an error span is seen. Spans arriving for that trace within this window
	// are always kept regardless of operation rate, preserving full context.
	errorTraceTTL = 60 * time.Second
)

// IngestSampler applies per-operation rate limiting at ingest time so that
// high-frequency non-error spans are dropped before they ever reach the spool.
//
// Rules:
//  1. If the span belongs to a trace that has had an error → always keep.
//  2. If the span's status is "error" → always keep AND mark the trace.
//  3. Otherwise → keep only if this (project, operation) is below the rate cap.
type IngestSampler struct {
	mu           sync.Mutex
	windows      map[string]*rateWindow
	errorTraces  map[string]time.Time // traceID → expiry
	maxPerMinute int
}

type rateWindow struct {
	count   int
	resetAt time.Time
}

// NewIngestSampler creates a sampler with the default rate cap.
func NewIngestSampler() *IngestSampler {
	s := &IngestSampler{
		windows:      make(map[string]*rateWindow),
		errorTraces:  make(map[string]time.Time),
		maxPerMinute: DefaultMaxPerMinute,
	}
	go s.gcLoop()
	return s
}

// Keep reports whether the span should be ingested.
// projectID and operation identify the rate bucket.
// status should be "error"/"ERROR"/"Error" for error spans.
// traceID is used to maintain trace integrity when errors are involved.
func (s *IngestSampler) Keep(projectID int64, operation, status, traceID string) bool {
	isError := strings.EqualFold(status, "error")

	s.mu.Lock()
	defer s.mu.Unlock()

	// Rule 1: trace is known to have an error → keep everything from it.
	if exp, ok := s.errorTraces[traceID]; ok && time.Now().Before(exp) {
		if isError {
			s.errorTraces[traceID] = time.Now().Add(errorTraceTTL)
		}
		return true
	}

	// Rule 2: this span is an error → mark the trace and keep.
	if isError {
		s.errorTraces[traceID] = time.Now().Add(errorTraceTTL)
		return true
	}

	// Rule 3: rate-based decision.
	key := fmt.Sprintf("%d\x00%s", projectID, operation)
	now := time.Now()
	w := s.windows[key]
	if w == nil || now.After(w.resetAt) {
		s.windows[key] = &rateWindow{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	w.count++
	return w.count <= s.maxPerMinute
}

// gcLoop periodically removes expired entries to prevent unbounded growth.
func (s *IngestSampler) gcLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		s.mu.Lock()
		for id, exp := range s.errorTraces {
			if now.After(exp) {
				delete(s.errorTraces, id)
			}
		}
		for key, w := range s.windows {
			if now.After(w.resetAt) {
				delete(s.windows, key)
			}
		}
		s.mu.Unlock()
	}
}
