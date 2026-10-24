package alert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// failingAggRepo fails every aggregate read with err while it is set.
type failingAggRepo struct {
	*mockAlertRepo
	err error
}

func (f *failingAggRepo) QueryAggregates(filter repository.AggregateFilter) ([]repository.Aggregate, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.mockAlertRepo.QueryAggregates(filter)
}

func evalFailEvaluator(err error) (*Evaluator, *failingAggRepo, *bytes.Buffer) {
	base := newMockRepo()
	base.alerts = []repository.Alert{{
		ID: 1, ProjectID: 1, Type: "error_rate", Service: "svc",
		ComparisonWindow: 5, CooldownMinutes: 30, Enabled: true, LabelFilters: "{}",
	}}
	repo := &failingAggRepo{mockAlertRepo: base, err: err}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	return NewEvaluator(repo, &mockNotifier{}, logger, nil), repo, &buf
}

func countLevel(buf *bytes.Buffer, level string) int {
	return strings.Count(buf.String(), "level="+level)
}

func TestEvaluateTimeoutLogsWarnUntilRepeated(t *testing.T) {
	timeout := fmt.Errorf("query current aggregate: %w", context.DeadlineExceeded)
	eval, _, buf := evalFailEvaluator(timeout)

	for i := 1; i < evalFailConfirm; i++ {
		if err := eval.Evaluate(context.Background(), 1); err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if n := countLevel(buf, "ERROR"); n != 0 {
			t.Fatalf("after %d timeouts: %d ERROR logs, want 0", i, n)
		}
	}
	if n := countLevel(buf, "WARN"); n != evalFailConfirm-1 {
		t.Fatalf("WARN logs = %d, want %d", n, evalFailConfirm-1)
	}
	if err := eval.Evaluate(context.Background(), 1); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if n := countLevel(buf, "ERROR"); n != 1 {
		t.Fatalf("after %d timeouts in a row: %d ERROR logs, want 1", evalFailConfirm, n)
	}
}

func TestEvaluateTimeoutCountResetsOnSuccess(t *testing.T) {
	timeout := fmt.Errorf("query current aggregate: %w", context.DeadlineExceeded)
	eval, repo, buf := evalFailEvaluator(timeout)

	for i := 0; i < evalFailConfirm-1; i++ {
		_ = eval.Evaluate(context.Background(), 1)
	}
	repo.err = nil
	_ = eval.Evaluate(context.Background(), 1)
	repo.err = timeout
	for i := 0; i < evalFailConfirm-1; i++ {
		_ = eval.Evaluate(context.Background(), 1)
	}
	if n := countLevel(buf, "ERROR"); n != 0 {
		t.Fatalf("ERROR logs = %d, want 0 after a success reset the count", n)
	}
}

func TestEvaluateOtherErrorLogsErrorAtOnce(t *testing.T) {
	eval, _, buf := evalFailEvaluator(errors.New("disk I/O error"))
	_ = eval.Evaluate(context.Background(), 1)
	if n := countLevel(buf, "ERROR"); n != 1 {
		t.Fatalf("ERROR logs = %d, want 1 for a non-timeout error", n)
	}
}
