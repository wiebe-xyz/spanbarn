package aggregation

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// MetricRepeatFilter drops raw cumulative points that repeat the previous
// point of their stream.
//
// An OTLP exporter sends every cumulative series on every export, changed or
// not. On 2026-10-24, 1,566 of 1,580 points of profotograaf-prod's
// http.server.request.duration repeated the stream's previous value, and raw
// metrics came to about 3.2 GB a day.
//
// A stream is one series from one process: project, name, attribute set and
// start time. Two pods that export the same attribute set have different
// start times, so they never mix. Only cumulative sums and histograms are
// filtered; gauges, delta points and points without a start time pass.
//
// Rates stay exact. A repeat is held, not dropped outright: when the stream
// changes, the held point is written just before the change, so the increase
// is measured over the export interval in which it happened. A stream that
// does not change still writes one point per heartbeat, so it stays visible
// in a short query window. The rollup accumulator sees every point either way;
// the filter only applies to the raw table.
type MetricRepeatFilter struct {
	heartbeat uint64 // nanoseconds
	now       func() time.Time

	mu        sync.Mutex
	streams   map[repeatKey]*repeatStream
	lastSweep time.Time
}

type repeatKey struct {
	projectID int64
	name      string
	attrs     string
	start     uint64
}

type repeatStream struct {
	value   float64
	count   uint64
	extra   []byte
	written uint64 // TimeUnixNano of the last point written
	held    *model.MetricRecord
	seen    time.Time
}

// NewMetricRepeatFilter returns a filter that writes an unchanged stream once
// per heartbeat. A heartbeat of zero or less disables it: Keep returns its
// input.
func NewMetricRepeatFilter(heartbeat time.Duration) *MetricRepeatFilter {
	if heartbeat <= 0 {
		return nil
	}
	return &MetricRepeatFilter{
		heartbeat: uint64(heartbeat),
		now:       time.Now,
		streams:   map[repeatKey]*repeatStream{},
	}
}

// Keep returns the points of recs to write, in order. A nil filter keeps all.
func (f *MetricRepeatFilter) Keep(recs []model.MetricRecord) []model.MetricRecord {
	if f == nil {
		return recs
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	out := make([]model.MetricRecord, 0, len(recs))
	for _, rec := range recs {
		out = f.keep(out, rec, now)
	}
	f.sweep(now)
	return out
}

func (f *MetricRepeatFilter) keep(out []model.MetricRecord, rec model.MetricRecord, now time.Time) []model.MetricRecord {
	if !repeatFiltered(rec) {
		return append(out, rec)
	}
	key := repeatKey{rec.ProjectID, rec.Name, string(rec.Attributes), rec.StartTimeUnixNano}
	st := f.streams[key]
	if st == nil {
		f.streams[key] = &repeatStream{value: rec.Value, count: rec.Count, extra: rec.Extra, written: rec.TimeUnixNano, seen: now}
		return append(out, rec)
	}
	st.seen = now
	if st.value == rec.Value && st.count == rec.Count && bytes.Equal(st.extra, rec.Extra) {
		if rec.TimeUnixNano >= st.written+f.heartbeat {
			st.written, st.held = rec.TimeUnixNano, nil
			return append(out, rec)
		}
		held := rec
		st.held = &held
		return out
	}
	if st.held != nil {
		out = append(out, *st.held)
	}
	st.value, st.count, st.extra = rec.Value, rec.Count, rec.Extra
	st.written, st.held = rec.TimeUnixNano, nil
	return append(out, rec)
}

// repeatFiltered reports whether rec belongs to a stream the filter tracks.
func repeatFiltered(rec model.MetricRecord) bool {
	if rec.Temporality != "cumulative" || rec.StartTimeUnixNano == 0 {
		return false
	}
	switch rec.Type {
	case model.MetricTypeSum, model.MetricTypeHistogram, model.MetricTypeExponentialHistogram:
		return true
	}
	return false
}

// sweep forgets streams not seen for three heartbeats, such as those of a
// process that restarted with a new start time. A forgotten stream's held
// point is dropped; the stream wrote a point at most one heartbeat before it.
func (f *MetricRepeatFilter) sweep(now time.Time) {
	idle := 3 * time.Duration(f.heartbeat)
	if now.Sub(f.lastSweep) < idle {
		return
	}
	f.lastSweep = now
	for k, st := range f.streams {
		if now.Sub(st.seen) >= idle {
			delete(f.streams, k)
		}
	}
}

// MetricWriter is the raw metrics insert that a MetricRepeatFilter wraps.
type MetricWriter interface {
	InsertMetrics(ctx context.Context, recs []model.MetricRecord) error
}

// FilteredMetricWriter writes through w the points f keeps.
type FilteredMetricWriter struct {
	w MetricWriter
	f *MetricRepeatFilter
}

// NewFilteredMetricWriter wraps w with f. A nil f writes every point.
func NewFilteredMetricWriter(w MetricWriter, f *MetricRepeatFilter) *FilteredMetricWriter {
	return &FilteredMetricWriter{w: w, f: f}
}

// InsertMetrics writes the points of recs that f keeps.
func (fw *FilteredMetricWriter) InsertMetrics(ctx context.Context, recs []model.MetricRecord) error {
	recs = fw.f.Keep(recs)
	if len(recs) == 0 {
		return nil
	}
	return fw.w.InsertMetrics(ctx, recs)
}
