package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// A filtered attribute set with every JSON type a stored attribute can hold.
const filterAttrs = `{"http.response.status_code":200,"ok":true,"off":false,"ratio":1.5,"route":"/a","code":"200"}`

var attrFilterCases = []struct {
	key, value string
	want       bool
}{
	{"http.response.status_code", "200", true},
	{"http.response.status_code", "201", false},
	{"ratio", "1.5", true},
	{"route", "/a", true},
	{"route", "/b", false},
	{"code", "200", true},
	{"ok", "true", true},
	{"ok", "false", false},
	{"ok", "1", false},
	{"off", "false", true},
	{"off", "0", false},
	{"missing", "200", false},
}

// checkAttrFilters runs every case through one query, which returns how many rows
// the filter kept.
func checkAttrFilters(t *testing.T, count func(attrs map[string]string) (int, error)) {
	t.Helper()
	for _, tc := range attrFilterCases {
		n, err := count(map[string]string{tc.key: tc.value})
		if err != nil {
			t.Fatalf("%s=%s: %v", tc.key, tc.value, err)
		}
		if got := n > 0; got != tc.want {
			t.Errorf("%s=%s: %d rows, want a match: %v", tc.key, tc.value, n, tc.want)
		}
	}
}

// A numeric attribute never matched on a raw read, and the empty result looked
// like missing data while the response labels showed the value as a string.
func TestQueryMetricSeriesFiltersEveryAttributeType(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	rec := makeMetricRecord(1, "m", model.MetricTypeGauge, 1)
	rec.Attributes = json.RawMessage(filterAttrs)
	if err := repo.InsertMetrics(ctx, []model.MetricRecord{rec}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	now := time.Now().UTC()
	checkAttrFilters(t, func(attrs map[string]string) (int, error) {
		rows, err := repo.QueryMetricSeries(ctx, MetricFilter{
			ProjectID: 1, Name: "m", From: now.Add(-time.Hour), To: now.Add(time.Hour), Attributes: attrs,
		})
		return len(rows), err
	})
}

func TestQueryLogsFiltersEveryAttributeType(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	rec := makeLogRecord(1, "", "line", 9)
	rec.Attributes = json.RawMessage(filterAttrs)
	if err := repo.InsertLogs(ctx, []model.LogRecord{rec}); err != nil {
		t.Fatalf("InsertLogs: %v", err)
	}
	now := time.Now().UTC()
	checkAttrFilters(t, func(attrs map[string]string) (int, error) {
		rows, _, err := repo.QueryLogs(ctx, LogFilter{
			ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour), Attributes: attrs,
		})
		return len(rows), err
	})
}

// The rollup tiers store every label as a string, so a filter that worked there
// keeps working: the values are the ones ParseAttributes wrote.
func TestQueryRollupsFilterStringifiedAttributes(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	stored := `{"code":"200","http.response.status_code":"200","off":"false","ok":"true","ratio":"1.5","route":"/a"}`
	if err := repo.UpsertMetricRollups([]MetricRollup{
		{ProjectID: 1, Name: "m", Type: "gauge", AttrFingerprint: "a", Attributes: stored, Bucket: base, Count: 1},
	}); err != nil {
		t.Fatalf("UpsertMetricRollups: %v", err)
	}
	if err := repo.UpsertCoarseRollups(ctx, []MetricRollup{
		{ProjectID: 1, Name: "m", Type: "gauge", AttrFingerprint: "a", Attributes: stored, Bucket: base, StepSeconds: 3600, Count: 1},
	}); err != nil {
		t.Fatalf("UpsertCoarseRollups: %v", err)
	}
	from, to := base.Add(-time.Hour), base.Add(time.Hour)

	t.Run("5m tier", func(t *testing.T) {
		checkStringifiedFilters(t, func(attrs map[string]string) (int, error) {
			rows, err := repo.QueryMetricRollups(ctx, MetricRollupFilter{ProjectID: 1, Name: "m", From: from, To: to, Attributes: attrs})
			return len(rows), err
		})
	})
	t.Run("coarse tier", func(t *testing.T) {
		checkStringifiedFilters(t, func(attrs map[string]string) (int, error) {
			rows, err := repo.QueryCoarseRollups(ctx, CoarseRollupFilter{ProjectID: 1, Name: "m", StepSeconds: 3600, From: from, To: to, Attributes: attrs})
			return len(rows), err
		})
	})
}

func checkStringifiedFilters(t *testing.T, count func(attrs map[string]string) (int, error)) {
	t.Helper()
	for k, v := range map[string]string{"http.response.status_code": "200", "ok": "true", "off": "false", "ratio": "1.5", "route": "/a"} {
		if n, err := count(map[string]string{k: v}); err != nil || n != 1 {
			t.Errorf("%s=%s: %d rows, err %v, want 1", k, v, n, err)
		}
	}
	if n, err := count(map[string]string{"http.response.status_code": "201"}); err != nil || n != 0 {
		t.Errorf("status 201: %d rows, err %v, want 0", n, err)
	}
}

func TestAppendAttrFiltersRejectsAnUnsafeKey(t *testing.T) {
	if _, _, err := appendAttrFilters(nil, nil, map[string]string{`a'b`: "1"}); err == nil {
		t.Fatal("a key with a quote reached the query")
	}
}
