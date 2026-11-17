package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogSlowTotalsNamesTheReadWithoutValues(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	req := metricRequest{
		name: "claude_code.cost.usage", projectID: 7, groupBy: []string{"session.id"},
		labels: map[string]string{"repo": "secret-value"},
		from:   time.Now().Add(-48 * time.Hour), to: time.Now(),
	}

	logSlowTotals(log, req, time.Second, 3)
	if buf.Len() != 0 {
		t.Fatalf("a fast read logged: %s", buf.String())
	}

	logSlowTotals(log, req, 5*time.Second, 3)
	out := buf.String()
	for _, want := range []string{"slow metric totals read", "project_id=7", "metric=claude_code.cost.usage", "group_by=session.id", "label_keys=repo", "took_ms=5000"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, "secret-value") {
		t.Errorf("log carries a label value: %s", out)
	}
}
