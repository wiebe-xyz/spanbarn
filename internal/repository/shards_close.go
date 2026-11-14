package repository

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

// The final checkpoint of an ended shard resets its WAL before the handle
// closes. On 2026-11-01 and 2026-11-04 it failed at midnight with no cause
// logged, under a budget of 10s and with no second try (#501, SPA-89). The
// budget and the retry below give it 41s in all, inside the 90s grace period
// even when a shutdown lands on the close.
const (
	// endedCheckpointBudget is the time one attempt may take.
	endedCheckpointBudget = 20 * time.Second
	// endedCheckpointAttempts counts the first try and the retry.
	endedCheckpointAttempts = 2
	// endedCheckpointPause is the wait ahead of the retry.
	endedCheckpointPause = time.Second
)

// finalCheckpoint resets the WAL of the ended shard h, trying once more if
// the first attempt leaves it in place. Only a WAL still in place after the
// retry logs at error level; the message is the one SPA-89 groups on.
func (m *ShardManager) finalCheckpoint(h *engine.DB, file string) {
	for attempt := 1; ; attempt++ {
		r := h.CheckpointEnded(m.attemptBudget(), m.logger)
		switch {
		case r.Cause == "":
			if attempt > 1 {
				m.logger.Warn("close ended shard: final checkpoint recovered on retry", r.Attrs(file, attempt)...)
			}
			return
		case attempt >= endedCheckpointAttempts:
			m.logger.Error("close ended shard: final checkpoint failed", r.Attrs(file, attempt)...)
			return
		}
		m.logger.Warn("close ended shard: final checkpoint failed, retrying", r.Attrs(file, attempt)...)
		if m.beforeCheckpointRetry != nil {
			m.beforeCheckpointRetry()
		}
		time.Sleep(endedCheckpointPause)
	}
}

// attemptBudget is the time one final checkpoint attempt may take. It is read
// per attempt, so a test can give the retry a budget its machine can meet.
func (m *ShardManager) attemptBudget() time.Duration {
	if m.closeBudget == 0 {
		return endedCheckpointBudget
	}
	return m.closeBudget
}
