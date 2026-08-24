package ingest

// Capacity management for TraceBuffer: which traces are sacrificed when the
// span cap is reached, and how that loss is accounted for and reported.
//
// The cap used to be enforced by refusing trace IDs the buffer had not seen
// before. That is admission refusal, not eviction, and it falls hardest on
// producers that emit their whole trace in a single batch — a CronJob gets one
// chance per day and mostly loses, while a long-lived service gets many. See
// the TraceBuffer doc comment.

// ensureRoom frees space for one more span, evicting if necessary. It reports
// whether the buffer is now below its cap. keep is the trace the span belongs
// to, if it is already buffered — a trace must never free space by evicting
// itself, which would drop the very spans the caller is about to append.
// Caller holds the lock.
func (tb *TraceBuffer) ensureRoom(keep *bufferedTrace) bool {
	for tb.maxSpans > 0 && tb.spanCount >= tb.maxSpans {
		if !tb.evictOne(keep) {
			return false
		}
	}
	return true
}

// evictOne drops one buffered trace, preferring traces sampling would discard
// anyway, and reports whether it found one. Error traces are never evicted.
// Caller holds the lock.
func (tb *TraceBuffer) evictOne(keep *bufferedTrace) bool {
	if tr := tb.nextVictim(&tb.sacrificeQ, &tb.sacrificeHead, true, keep); tr != nil {
		tb.evictedSacrif += int64(len(tr.spans))
		tb.evictedSTotal += int64(len(tr.spans))
		tb.remove(tr)
		return true
	}
	if tr := tb.nextVictim(&tb.anyQ, &tb.anyHead, false, keep); tr != nil {
		tb.evictedKept += int64(len(tr.spans))
		tb.evictedKTotal += int64(len(tr.spans))
		tb.remove(tr)
		return true
	}
	return false
}

// nextVictim pops queued trace IDs until it finds one that is still buffered
// and may be evicted. Entries for traces that have since been flushed, evicted,
// or latched as errors are discarded on the way past. Caller holds the lock.
func (tb *TraceBuffer) nextVictim(queue *[]string, head *int, sacrificialOnly bool, keep *bufferedTrace) *bufferedTrace {
	q := *queue
	for *head < len(q) {
		tr := tb.traces[q[*head]]
		*head++
		if tr == nil || tr.hasError || tr == keep {
			continue
		}
		if sacrificialOnly && !tr.sacrificial {
			continue
		}
		tb.compact(queue, head)
		return tr
	}
	tb.compact(queue, head)
	return nil
}

// compact reclaims the consumed prefix of an eviction queue once it is worth
// the copy. Caller holds the lock.
func (tb *TraceBuffer) compact(queue *[]string, head *int) {
	q := *queue
	if *head < evictQueueCompactAt || *head*2 < len(q) {
		return
	}
	*queue = append(q[:0], q[*head:]...)
	*head = 0
}

// remove deletes a trace from the buffer and returns its span budget to the
// cap. Its eviction-queue entries are left behind and skipped lazily.
// Caller holds the lock.
func (tb *TraceBuffer) remove(tr *bufferedTrace) {
	delete(tb.traces, tr.id)
	tb.spanCount -= len(tr.spans)
	if tb.spanCount < 0 {
		tb.spanCount = 0
	}
}

// lossWindow is the loss accounted since the previous flush.
type lossWindow struct {
	refused       int64
	evictedSacrif int64
	evictedKept   int64
	maxSpans      int
	buffered      int
	traces        int
}

// takeLossWindow reads and resets the windowed loss counters. Caller holds the
// lock.
func (tb *TraceBuffer) takeLossWindow() lossWindow {
	w := lossWindow{
		refused:       tb.refused,
		evictedSacrif: tb.evictedSacrif,
		evictedKept:   tb.evictedKept,
		maxSpans:      tb.maxSpans,
	}
	tb.refused, tb.evictedSacrif, tb.evictedKept = 0, 0, 0
	return w
}

// reportLoss logs what the cap cost during the last window, at a level that
// matches how much it actually cost. Shedding traces that sampling was going to
// throw away is the cap working as designed and is reported at info; losing
// traces that would have been stored, or refusing spans outright, is not.
func (tb *TraceBuffer) reportLoss(w lossWindow) {
	if w.evictedSacrif > 0 && w.evictedKept == 0 && w.refused == 0 {
		tb.logger.Info("trace buffer at capacity, shed unsampled traces",
			"evicted_spans", w.evictedSacrif,
			"max_spans", w.maxSpans,
			"buffered_spans", w.buffered,
			"buffered_traces", w.traces)
		return
	}
	if w.evictedKept == 0 && w.refused == 0 {
		return
	}
	tb.logger.Warn("trace buffer over capacity, telemetry lost",
		"evicted_kept_spans", w.evictedKept,
		"refused_spans", w.refused,
		"evicted_unsampled_spans", w.evictedSacrif,
		"max_spans", w.maxSpans,
		"buffered_spans", w.buffered,
		"buffered_traces", w.traces,
		"hint", "raise SPANBARN_TRACE_BUFFER_MAX_SPANS or lower the ingest sample ratio")
}

// pruneEvictionQueues drops queue entries for traces that are no longer
// buffered, so the queues cannot grow without bound when nothing is evicting.
// Insertion order is preserved. Caller holds the lock.
func (tb *TraceBuffer) pruneEvictionQueues() {
	tb.sacrificeQ, tb.sacrificeHead = tb.pruneQueue(tb.sacrificeQ, tb.sacrificeHead), 0
	tb.anyQ, tb.anyHead = tb.pruneQueue(tb.anyQ, tb.anyHead), 0
}

func (tb *TraceBuffer) pruneQueue(q []string, head int) []string {
	kept := q[:0]
	for _, id := range q[head:] {
		if tb.traces[id] != nil {
			kept = append(kept, id)
		}
	}
	return kept
}
