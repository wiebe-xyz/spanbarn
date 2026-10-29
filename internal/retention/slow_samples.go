package retention

import (
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// A span that is only slow is copied to error_samples for at most
// SlowSampleTraces traces per (project, service, operation) and ingest hour.
// Without the cap, an operation that is slow by nature had every span copied:
// a blocking queue receive or an LLM request always runs past the threshold.
// On prod (2026-10-29) 68% of error_samples were one service's queue.receive
// spans, and only 154 of 50,000 recent samples were errors. The logs of every
// sampled trace are kept for error_log_retention_days on top, so the copies
// also grew kept_logs by about 80,000 rows a day.

// slowKey is the scope one slow-sample cap counts traces in.
type slowKey struct {
	project int64
	service string
	name    string
	hour    int64
}

// slowSampler remembers which traces each scope admitted. Retention drains
// spans oldest first, so an hour stops receiving spans soon after the next
// one starts; scopes two hours behind the newest are forgotten.
type slowSampler struct {
	mu     sync.Mutex
	traces map[slowKey]map[string]struct{}
	newest int64
}

// slowScopeHours is how many hours behind the newest a scope is remembered.
const slowScopeHours = 2

// admit reports whether s, a span that is only slow, is copied. A trace a
// scope admitted keeps being admitted there, so a retried batch and the other
// spans of an admitted trace are copied as well.
func (p *slowSampler) admit(s repository.Span, limit int) bool {
	key := slowKey{s.ProjectID, s.Service, s.Name, s.IngestedAt.Unix() / int64(time.Hour/time.Second)}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.traces == nil {
		p.traces = make(map[slowKey]map[string]struct{})
	}
	if key.hour > p.newest {
		p.newest = key.hour
		p.forgetBefore(key.hour - slowScopeHours)
	}
	ids := p.traces[key]
	if _, ok := ids[s.TraceID]; ok {
		return true
	}
	if len(ids) >= limit {
		return false
	}
	if ids == nil {
		ids = make(map[string]struct{}, limit)
		p.traces[key] = ids
	}
	ids[s.TraceID] = struct{}{}
	return true
}

func (p *slowSampler) forgetBefore(hour int64) {
	for k := range p.traces {
		if k.hour < hour {
			delete(p.traces, k)
		}
	}
}

// samplesOf returns the spans of batch that go to error_samples: every error
// and durable span, and slow spans up to the cap. It also returns how many
// slow spans the cap left out.
func (w *RetentionWorker) samplesOf(cfg Config, batch []repository.Span) ([]repository.Span, int64) {
	var samples []repository.Span
	var skipped int64
	for _, s := range batch {
		switch {
		case s.Status == "error" || s.Durable:
			samples = append(samples, s)
		case s.DurationUs > cfg.SlowThresholdUS:
			if w.slow.admit(s, cfg.SlowSampleTraces) {
				samples = append(samples, s)
			} else {
				skipped++
			}
		}
	}
	return samples, skipped
}
