package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

func makeMetricRecord(projectID int64, name string, typ model.MetricType, value float64) model.MetricRecord {
	attrs, _ := json.Marshal(map[string]string{"service.name": "test-svc"})
	return model.MetricRecord{
		ProjectID:    projectID,
		Name:         name,
		Description:  "test metric",
		Unit:         "ms",
		Type:         typ,
		TimeUnixNano: uint64(time.Now().UnixNano()),
		Value:        value,
		Count:        10,
		Attributes:   attrs,
	}
}

func TestInsertMetricsEmpty(t *testing.T) {
	repo := setupTestDB(t)
	if err := repo.InsertMetrics(context.Background(), nil); err != nil {
		t.Fatalf("InsertMetrics(nil): %v", err)
	}
}

func TestInsertAndQueryAllMetricTypes(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	histExtra, _ := json.Marshal(map[string]any{
		"bounds": []float64{0, 5, 10, 25, 50, 100},
		"counts": []uint64{0, 2, 5, 8, 3, 1},
	})
	summaryExtra, _ := json.Marshal(map[string]any{
		"quantiles": []map[string]float64{
			{"quantile": 0.5, "value": 12.3},
			{"quantile": 0.99, "value": 45.6},
		},
	})
	expHistExtra, _ := json.Marshal(map[string]any{
		"scale":      3,
		"zero_count": 0,
		"positive":   map[string]any{"offset": 0, "bucket_counts": []uint64{1, 2, 3}},
	})

	recs := []model.MetricRecord{
		makeMetricRecord(1, "cpu.usage", model.MetricTypeGauge, 0.72),
		makeMetricRecord(1, "requests.total", model.MetricTypeSum, 1500),
		{
			ProjectID:    1,
			Name:         "http.duration",
			Type:         model.MetricTypeHistogram,
			TimeUnixNano: uint64(now.UnixNano()),
			Value:        1234.5,
			Count:        19,
			Attributes:   json.RawMessage(`{"service.name":"web"}`),
			Extra:        histExtra,
		},
		{
			ProjectID:    1,
			Name:         "latency.exp",
			Type:         model.MetricTypeExponentialHistogram,
			TimeUnixNano: uint64(now.UnixNano()),
			Value:        500.0,
			Count:        6,
			Attributes:   json.RawMessage(`{"service.name":"web"}`),
			Extra:        expHistExtra,
		},
		{
			ProjectID:    1,
			Name:         "response.size",
			Type:         model.MetricTypeSummary,
			TimeUnixNano: uint64(now.UnixNano()),
			Value:        9876.0,
			Count:        42,
			Attributes:   json.RawMessage(`{"service.name":"web"}`),
			Extra:        summaryExtra,
		},
	}

	if err := repo.InsertMetrics(context.Background(), recs); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	for _, rec := range recs {
		rows, err := repo.QueryMetricSeries(context.Background(), MetricFilter{
			ProjectID: 1,
			Name:      rec.Name,
			From:      now.Add(-time.Minute),
			To:        now.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("QueryMetricSeries(%s): %v", rec.Name, err)
		}
		if len(rows) != 1 {
			t.Fatalf("QueryMetricSeries(%s): want 1 row, got %d", rec.Name, len(rows))
		}
		if rows[0].Type != string(rec.Type) {
			t.Errorf("%s: type want %q, got %q", rec.Name, rec.Type, rows[0].Type)
		}
		if rows[0].Value != rec.Value {
			t.Errorf("%s: value want %v, got %v", rec.Name, rec.Value, rows[0].Value)
		}
		if rec.Extra != nil && rows[0].Extra == "" {
			t.Errorf("%s: extra should be non-empty", rec.Name)
		}
	}
}

func TestListMetricNames(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	recs := []model.MetricRecord{
		makeMetricRecord(1, "alpha", model.MetricTypeGauge, 1),
		makeMetricRecord(1, "beta", model.MetricTypeSum, 2),
		makeMetricRecord(1, "alpha", model.MetricTypeGauge, 3), // duplicate name
		makeMetricRecord(2, "gamma", model.MetricTypeGauge, 4), // different project
	}
	if err := repo.InsertMetrics(context.Background(), recs); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	names, err := repo.ListMetricNames(context.Background(), 1, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListMetricNames: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("want 2 distinct names for project 1, got %d: %v", len(names), names)
	}
	if names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("want [alpha beta], got %v", names)
	}

	// Project 2 should only see gamma.
	names2, err := repo.ListMetricNames(context.Background(), 2, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ListMetricNames p2: %v", err)
	}
	if len(names2) != 1 || names2[0] != "gamma" {
		t.Errorf("want [gamma], got %v", names2)
	}
}

func TestQueryMetricSeriesLabelFilter(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	svcA, _ := json.Marshal(map[string]string{"service.name": "svc-a"})
	svcB, _ := json.Marshal(map[string]string{"service.name": "svc-b"})

	recs := []model.MetricRecord{
		{ProjectID: 1, Name: "req", Type: model.MetricTypeSum, TimeUnixNano: uint64(now.UnixNano()), Value: 10, Attributes: svcA},
		{ProjectID: 1, Name: "req", Type: model.MetricTypeSum, TimeUnixNano: uint64(now.UnixNano()), Value: 20, Attributes: svcB},
	}
	if err := repo.InsertMetrics(context.Background(), recs); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	rows, err := repo.QueryMetricSeries(context.Background(), MetricFilter{
		ProjectID:  1,
		Name:       "req",
		From:       now.Add(-time.Minute),
		To:         now.Add(time.Minute),
		Attributes: map[string]string{"service.name": "svc-a"},
	})
	if err != nil {
		t.Fatalf("QueryMetricSeries: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row for svc-a, got %d", len(rows))
	}
	if rows[0].Value != 10 {
		t.Errorf("want value 10, got %v", rows[0].Value)
	}
}

func TestDeleteMetricsOlderThan(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()

	if err := repo.InsertMetrics(context.Background(), []model.MetricRecord{
		makeMetricRecord(1, "old", model.MetricTypeGauge, 1),
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	// Nothing should be deleted yet.
	n, err := repo.DeleteMetricsOlderThan(context.Background(), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("DeleteMetricsOlderThan: %v", err)
	}
	if n != 0 {
		t.Errorf("want 0 deleted, got %d", n)
	}

	// Delete everything ingested before a future timestamp.
	n, err = repo.DeleteMetricsOlderThan(context.Background(), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteMetricsOlderThan future: %v", err)
	}
	if n != 1 {
		t.Errorf("want 1 deleted, got %d", n)
	}

	// Table should be empty now.
	rows, err := repo.QueryMetricSeries(context.Background(), MetricFilter{
		ProjectID: 1, Name: "old",
		From: now.Add(-time.Hour), To: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryMetricSeries after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("want 0 rows after delete, got %d", len(rows))
	}
}
