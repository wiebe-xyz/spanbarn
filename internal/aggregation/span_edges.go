package aggregation

import (
	"log/slog"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/deptarget"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// spanEdges folds the calls a batch of spans shows into 5-minute buckets of
// span_edges, next to span_rollups. A batch is one staging flush of whole
// traces, or one batch of the inline worker paths, so a span finds its parent
// when both arrived in the same batch.
//
// Every bucket a span arrived in gets a marker row on the next flush, calls
// or not, so a reader can tell a quiet bucket from one no writer covered.
type spanEdges struct {
	mu      sync.Mutex
	slots   map[spanEdgeKey]*spanEdgeSlot
	buckets map[int64]struct{}
	caps    rollupCaps
	cfg     SpanRollupConfig // MaxEdgeTargets and MaxServices cap the rows
	writer  SpanRollupWriter
	logger  *slog.Logger
	now     func() time.Time
}

type spanEdgeKey struct {
	bucket                     int64
	projectID                  int64
	source, target, targetType string
}

type spanEdgeSlot struct {
	durationStats
	errors int64
}

func newSpanEdges(w SpanRollupWriter, cfg SpanRollupConfig, logger *slog.Logger, now func() time.Time) *spanEdges {
	return &spanEdges{
		slots:   map[spanEdgeKey]*spanEdgeSlot{},
		buckets: map[int64]struct{}{},
		caps:    newRollupCaps(),
		cfg:     cfg,
		writer:  w,
		logger:  logger,
		now:     now,
	}
}

// arrivalBucket is the 5-minute bucket a span arrived in.
func arrivalBucket(s repository.Span, now func() time.Time) int64 {
	at := s.IngestedAt
	if at.IsZero() {
		at = now()
	}
	return at.Unix() - at.Unix()%SpanRollupStep
}

// edgeSpans lets deptarget walk a batch.
type edgeSpans []repository.Span

func (v edgeSpans) Len() int { return len(v) }

func (v edgeSpans) At(i int) deptarget.Span {
	sp := &v[i]
	return deptarget.Span{TraceID: sp.TraceID, SpanID: sp.SpanID, ParentSpanID: sp.ParentSpanID,
		Service: sp.Service, Kind: sp.Kind, Attributes: sp.Attributes}
}

// add folds the calls of one batch. The attributes are parsed before the
// lock is taken.
func (e *spanEdges) add(spans []repository.Span) {
	var calls []deptarget.Call
	deptarget.Calls(edgeSpans(spans), func(c deptarget.Call) { calls = append(calls, c) })
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range spans {
		e.buckets[arrivalBucket(spans[i], e.now)] = struct{}{}
	}
	for _, c := range calls {
		e.addCall(c, spans[c.Span])
	}
}

// addCall folds one call carried by s. Past the caps, the target or the
// source and target fold into OtherName; the target type stays.
func (e *spanEdges) addCall(c deptarget.Call, s repository.Span) {
	bucket := arrivalBucket(s, e.now)
	capCfg := SpanRollupConfig{MaxNames: e.cfg.MaxEdgeTargets, MaxServices: e.cfg.MaxServices}
	source, target, first := e.caps.admit(s.ProjectID, c.Source, c.Target+"\x00"+c.TargetType, bucket, capCfg)
	if target != OtherName {
		target = c.Target
	}
	if first {
		e.logger.Info("span edges: cardinality cap reached, folding the rest of the hour into "+OtherName,
			"project_id", s.ProjectID, "source", c.Source, "max_targets", e.cfg.MaxEdgeTargets, "max_services", e.cfg.MaxServices)
	}
	k := spanEdgeKey{bucket: bucket, projectID: s.ProjectID, source: source, target: target, targetType: c.TargetType}
	sl := e.slots[k]
	if sl == nil {
		sl = &spanEdgeSlot{}
		e.slots[k] = sl
	}
	sl.observe(s)
	if rollupStatus(s.Status) == "error" {
		sl.errors++
	}
}

// flush writes every slot and a marker per bucket seen, then starts new ones.
// When the write fails they go back, unless their bucket is older than
// spanRollupRequeueAge.
func (e *spanEdges) flush() error {
	e.mu.Lock()
	slots, buckets := e.slots, e.buckets
	e.slots, e.buckets = map[spanEdgeKey]*spanEdgeSlot{}, map[int64]struct{}{}
	e.caps.prune(e.now().Unix())
	e.mu.Unlock()
	if len(buckets) == 0 && len(slots) == 0 {
		return nil
	}

	rows := make([]repository.SpanEdge, 0, len(slots)+len(buckets))
	for b := range buckets {
		rows = append(rows, repository.SpanEdgeMarker(SpanRollupStep, time.Unix(b, 0).UTC()))
	}
	for k, sl := range slots {
		rows = append(rows, sl.row(k))
	}
	start := e.now()
	if err := e.writer.AddSpanEdges(rows); err != nil {
		e.requeue(unwrittenSlots(slots, err, spanEdgeRowKey), buckets, err)
		return err
	}
	e.logger.Info("persisted span edges", "rows", len(rows), "duration_ms", e.now().Sub(start).Milliseconds())
	return nil
}

func (sl *spanEdgeSlot) row(k spanEdgeKey) repository.SpanEdge {
	return repository.SpanEdge{
		StepSeconds:   SpanRollupStep,
		Bucket:        time.Unix(k.bucket, 0).UTC(),
		ProjectID:     k.projectID,
		Source:        k.source,
		Target:        k.target,
		TargetType:    k.targetType,
		Count:         sl.count,
		ErrorCount:    sl.errors,
		WeightedCount: sl.weights.count,
		WeightedSpans: sl.weights.spans,
		SumDurationUs: sl.sum,
		MinUs:         sl.min,
		MaxUs:         sl.max,
		Hist:          sl.hist.Encode(),
	}
}

func spanEdgeRowKey(row repository.SpanEdge) spanEdgeKey {
	return spanEdgeKey{
		bucket:     row.Bucket.Unix(),
		projectID:  row.ProjectID,
		source:     row.Source,
		target:     row.Target,
		targetType: row.TargetType,
	}
}

func (e *spanEdges) requeue(slots map[spanEdgeKey]*spanEdgeSlot, buckets map[int64]struct{}, cause error) {
	oldest := e.now().Add(-spanRollupRequeueAge).Unix()
	e.mu.Lock()
	for b := range buckets {
		if b >= oldest {
			e.buckets[b] = struct{}{}
		}
	}
	lost := requeueSlots(e.slots, slots, func(k spanEdgeKey) bool { return k.bucket >= oldest },
		func(cur, sl *spanEdgeSlot) {
			cur.merge(sl.durationStats)
			cur.errors += sl.errors
		})
	e.mu.Unlock()
	if lost > 0 {
		e.logger.Error("span edges dropped after failed writes", "rows_lost", lost, "error", cause)
	}
}
