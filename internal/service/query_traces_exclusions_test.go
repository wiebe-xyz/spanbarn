package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// failingExclusions answers every saved-exclusion lookup with an error and
// passes everything else to the real repository.
type failingExclusions struct{ QueryRepository }

func (failingExclusions) ExcludedOperations(int64) ([]string, error) {
	return nil, errors.New("exclusions table unreadable")
}

// A failed lookup of the saved exclusions used to be dropped without a trace:
// excluded operations came back into trace search and groups and nothing said
// why. The service is the log boundary, so it logs at error level and still
// answers the search.
func TestTraceSearchLogsFailedExclusionLookup(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	svc := NewQueryService(failingExclusions{setupTestRepo(t)}, logger, nil)
	ctx := context.Background()
	filter := TraceSearchFilter{ProjectID: 1, To: time.Now().Add(time.Hour), Limit: 10}

	if _, err := svc.SearchTraces(ctx, filter); err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	if _, err := svc.ListTraceGroups(ctx, filter); err != nil {
		t.Fatalf("ListTraceGroups: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	logged := 0
	for _, line := range lines {
		if strings.Contains(line, `"level":"ERROR"`) && strings.Contains(line, "exclusions table unreadable") {
			logged++
		}
	}
	if logged != 2 {
		t.Fatalf("want 2 error log lines naming the lookup failure (search and groups), got %d in:\n%s", logged, buf.String())
	}
}
