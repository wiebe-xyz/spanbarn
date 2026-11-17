package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
)

// A metric read takes an environment: one environment reads its own series,
// production also reads series with no deployment.environment label, and an
// empty name or "all" reads every series (#558).
func TestQueryMetricSeriesEnvironment(t *testing.T) {
	repo := setupTestDB(t)
	ctx := context.Background()
	mk := func(attrs string) model.MetricRecord {
		rec := makeMetricRecord(1, "m", model.MetricTypeGauge, 1)
		rec.Attributes = json.RawMessage(attrs)
		return rec
	}
	if err := repo.InsertMetrics(ctx, []model.MetricRecord{
		mk(`{"deployment.environment":"production"}`),
		mk(`{"deployment.environment":"production","host":"b"}`),
		mk(`{"deployment.environment":"staging"}`),
		mk(`{"host":"legacy"}`),
		mk(`{"deployment.environment":""}`),
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	now := time.Now().UTC()
	for env, want := range map[string]int{"": 5, "all": 5, "production": 4, "staging": 1, "testing": 0} {
		rows, err := repo.QueryMetricSeries(ctx, MetricFilter{
			ProjectID: 1, Name: "m", From: now.Add(-time.Hour), To: now.Add(time.Hour), Environment: env,
		})
		if err != nil {
			t.Fatalf("environment %q: %v", env, err)
		}
		if len(rows) != want {
			t.Errorf("environment %q: %d rows, want %d", env, len(rows), want)
		}
	}
}
