package worker

import (
	"math/rand/v2"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
)

// classifyForStorage builds the set of spans to write to SQLite.
// All spans in error/slow traces are included unconditionally. Spans in
// verbose-mode projects bypass boring classification. Remaining boring traces
// are sampled whole at the per-project ratio — the die is rolled once per
// trace_id, and either all spans in that trace are kept or none are.
func (w *RedisWorker) classifyForStorage(spans []repository.Span) []repository.Span {
	return classifySpansForStorage(spans, w.cfg.SlowThresholdUs, w.cfg.BoringRetention, w.boringPolicy, w.floor, w.hourFloor)
}

// boringTrace groups the spans of one trace that has no error, slow or verbose
// span, so it can be kept or dropped as a whole.
type boringTrace struct {
	projectID int64
	spans     []repository.Span
}

// classifySpansForStorage builds the set of spans to persist from a set of spans
// that ideally covers whole traces. Extracted so the staging flusher can reuse
// the exact same trace-level classification the inline worker path uses.
func classifySpansForStorage(spans []repository.Span, slowThresholdUs int64, boringRetention time.Duration, boringPolicy BoringPolicyReader, floor, hourly *sampling.MinuteFloor) []repository.Span {
	if slowThresholdUs <= 0 {
		return spans
	}

	now := time.Now()
	verbose := verboseProjects(spans, boringPolicy, now)
	interesting := interestingTraceIDs(spans, verbose, slowThresholdUs)
	result, order, boringTraces := splitBoringTraces(spans, interesting)

	tier := newHourlyTier(boringPolicy, hourly)
	tier.markVerboseClean(result, verbose, slowThresholdUs)

	if boringPolicy == nil || len(order) == 0 {
		return result
	}
	sampler := newBoringSampler(boringPolicy, floor)
	for _, traceID := range order {
		bt := boringTraces[traceID]
		// A clean trace the hourly rarity tier admits is durable: it is kept
		// without an expiry and never touches the ratio sampler or minute floor.
		if tier.admit(bt.projectID, bt.spans) {
			result = append(result, bt.spans...)
			continue
		}
		if !sampler.keep(bt) {
			continue
		}
		stampBoringExpiry(bt.spans, boringRetention, now)
		result = append(result, bt.spans...)
	}
	return result
}

// hourlyTier is the hourly rarity tier: the first clean trace per (project,
// operation, hour) is marked durable so it outlives boring expiry. It is
// disabled by a nil policy or a nil floor.
type hourlyTier struct {
	policy   BoringPolicyReader
	floor    *sampling.MinuteFloor
	minCache map[int64]int
}

func newHourlyTier(policy BoringPolicyReader, floor *sampling.MinuteFloor) *hourlyTier {
	if policy == nil || floor == nil {
		return nil
	}
	return &hourlyTier{policy: policy, floor: floor, minCache: make(map[int64]int, 2)}
}

// admit consults the hourly floor for one clean trace. When it admits the trace,
// every span is marked Durable and true is returned. A nil tier admits nothing.
func (h *hourlyTier) admit(projectID int64, spans []repository.Span) bool {
	if h == nil || len(spans) == 0 {
		return false
	}
	min, ok := h.minCache[projectID]
	if !ok {
		min = h.policy.MinTracesPerHour(projectID)
		h.minCache[projectID] = min
	}
	root := traceRoot(spans)
	if !h.floor.ShouldKeep(projectID, root.Name, h.floor.BucketOf(root.StartTimeUs), min, false) {
		return false
	}
	for i := range spans {
		spans[i].Durable = true
	}
	return true
}

// markVerboseClean marks durable the traces in result that are interesting only
// because their project is verbose (no error span, no slow span) and that the
// hourly floor admits. Those traces are stored regardless; this only flags them.
func (h *hourlyTier) markVerboseClean(result []repository.Span, verbose map[int64]bool, slowThresholdUs int64) {
	if h == nil {
		return
	}
	type group struct {
		idx   []int
		dirty bool
	}
	groups := make(map[string]*group, 8)
	var order []string
	for i, s := range result {
		g := groups[s.TraceID]
		if g == nil {
			g = &group{}
			groups[s.TraceID] = g
			order = append(order, s.TraceID)
		}
		g.idx = append(g.idx, i)
		if s.Status == "error" || s.DurationUs > slowThresholdUs {
			g.dirty = true
		}
	}
	for _, id := range order {
		g := groups[id]
		if g.dirty || !verbose[result[g.idx[0]].ProjectID] {
			continue
		}
		spans := make([]repository.Span, len(g.idx))
		for j, i := range g.idx {
			spans[j] = result[i]
		}
		if h.admit(spans[0].ProjectID, spans) {
			for _, i := range g.idx {
				result[i].Durable = true
			}
		}
	}
}

// verboseProjects reports which projects in spans are in verbose mode, meaning
// all of their spans are recorded. It is empty without a policy.
func verboseProjects(spans []repository.Span, policy BoringPolicyReader, now time.Time) map[int64]bool {
	verbose := make(map[int64]bool, 2)
	if policy == nil {
		return verbose
	}
	for _, s := range spans {
		if _, seen := verbose[s.ProjectID]; !seen {
			until := policy.VerboseUntil(s.ProjectID)
			verbose[s.ProjectID] = !until.IsZero() && now.Before(until)
		}
	}
	return verbose
}

// interestingTraceIDs returns the trace ids holding at least one error span,
// slow span or span of a verbose project.
func interestingTraceIDs(spans []repository.Span, verbose map[int64]bool, slowThresholdUs int64) map[string]struct{} {
	interesting := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if verbose[s.ProjectID] || s.Status == "error" || s.DurationUs > slowThresholdUs {
			interesting[s.TraceID] = struct{}{}
		}
	}
	return interesting
}

// splitBoringTraces returns the spans of interesting traces in input order, and
// the remaining spans grouped by trace id with the trace ids in first-seen order.
func splitBoringTraces(spans []repository.Span, interesting map[string]struct{}) ([]repository.Span, []string, map[string]*boringTrace) {
	result := make([]repository.Span, 0, len(spans))
	var order []string
	boringTraces := make(map[string]*boringTrace, 8)
	for _, s := range spans {
		if _, ok := interesting[s.TraceID]; ok {
			result = append(result, s)
			continue
		}
		bt := boringTraces[s.TraceID]
		if bt == nil {
			bt = &boringTrace{projectID: s.ProjectID}
			boringTraces[s.TraceID] = bt
			order = append(order, s.TraceID)
		}
		bt.spans = append(bt.spans, s)
	}
	return result, order, boringTraces
}

// stampBoringExpiry marks sampled-boring spans with an expires_at so the
// cleanup can delete them with a plain indexed range scan. A non-positive
// retention leaves expires_at nil, so those spans fall back to the
// aggregate-then-delete pass.
func stampBoringExpiry(spans []repository.Span, retention time.Duration, now time.Time) {
	if retention <= 0 {
		return
	}
	for i := range spans {
		base := spans[i].IngestedAt
		if base.IsZero() {
			base = now
		}
		exp := base.Add(retention)
		spans[i].ExpiresAt = &exp
	}
}

// boringSampler decides per boring trace whether to keep it, caching the
// per-project policy lookups for the duration of one batch.
type boringSampler struct {
	policy     BoringPolicyReader
	floor      *sampling.MinuteFloor
	ratioCache map[int64]int
	minCache   map[int64]int
}

func newBoringSampler(policy BoringPolicyReader, floor *sampling.MinuteFloor) *boringSampler {
	return &boringSampler{
		policy:     policy,
		floor:      floor,
		ratioCache: make(map[int64]int, 2),
		minCache:   make(map[int64]int, 2),
	}
}

// keep reports whether the whole trace is retained.
func (b *boringSampler) keep(bt *boringTrace) bool {
	ratioKeep := b.ratioVerdict(bt.projectID)
	if b.floor == nil {
		return ratioKeep
	}
	// The per-(project, operation) minute floor rescues a minimum number of
	// boring traces each minute so quiet operations never vanish entirely —
	// even when ratio == 0 would otherwise drop every boring trace.
	min, ok := b.minCache[bt.projectID]
	if !ok {
		min = b.policy.MinTracesPerMinute(bt.projectID)
		b.minCache[bt.projectID] = min
	}
	op, minute := boringTraceKey(bt.spans)
	return b.floor.ShouldKeep(bt.projectID, op, minute, min, ratioKeep)
}

// ratioVerdict rolls the sample die for one trace: ratio 1 keeps all, N>1 keeps
// 1-in-N, 0 drops.
func (b *boringSampler) ratioVerdict(projectID int64) bool {
	ratio, ok := b.ratioCache[projectID]
	if !ok {
		ratio = b.policy.SampleRatio(projectID)
		b.ratioCache[projectID] = ratio
	}
	switch {
	case ratio == 1:
		return true
	case ratio > 1:
		return rand.IntN(ratio) == 0
	}
	return false
}

// boringTraceKey returns the operation name and wall-clock minute bucket used to
// group a boring trace for the survival floor. It prefers the root span (no
// parent), falling back to the first span in the batch.
func boringTraceKey(spans []repository.Span) (string, int64) {
	root := traceRoot(spans)
	return root.Name, root.StartTimeUs / 60_000_000
}

// traceRoot returns the root span (no parent) of a trace, falling back to the
// first span in the batch. It supplies the operation name and start time for
// both the minute floor and the hourly tier.
func traceRoot(spans []repository.Span) repository.Span {
	for _, s := range spans {
		if s.ParentSpanID == "" {
			return s
		}
	}
	return spans[0]
}

// classifyInteresting returns only the spans that should be written to SQLite:
// those that are errors or slow, plus any boring span that shares a trace_id
// with an interesting span (preserves waterfall completeness within a batch).
// Used by tests directly; production code uses classifyForStorage.
func classifyInteresting(spans []repository.Span, slowThresholdUs int64) []repository.Span {
	interestingTraces := make(map[string]struct{}, len(spans))
	for _, s := range spans {
		if s.Status == "error" || s.DurationUs > slowThresholdUs {
			interestingTraces[s.TraceID] = struct{}{}
		}
	}
	if len(interestingTraces) == 0 {
		return nil
	}
	result := make([]repository.Span, 0, len(spans))
	for _, s := range spans {
		if _, ok := interestingTraces[s.TraceID]; ok {
			result = append(result, s)
		}
	}
	return result
}
