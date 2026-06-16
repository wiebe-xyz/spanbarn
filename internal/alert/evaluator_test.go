package alert

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// mockNotifier records notification calls.
type mockNotifier struct {
	webhooks []webhookCall
	emails   []emailCall
}

type webhookCall struct {
	url     string
	payload AlertPayload
}

type emailCall struct {
	to      string
	subject string
	body    string
}

func (m *mockNotifier) SendWebhook(_ context.Context, url string, payload AlertPayload) error {
	m.webhooks = append(m.webhooks, webhookCall{url: url, payload: payload})
	return nil
}

func (m *mockNotifier) SendEmail(_ context.Context, to, subject, body string) error {
	m.emails = append(m.emails, emailCall{to: to, subject: subject, body: body})
	return nil
}

// mockAlertRepo is a simple in-memory mock for testing the evaluator.
type mockAlertRepo struct {
	alerts     []repository.Alert
	aggregates []repository.Aggregate
	rollups    []repository.MetricRollup
	triggered  map[int64]time.Time
}

func newMockRepo() *mockAlertRepo {
	return &mockAlertRepo{
		triggered: make(map[int64]time.Time),
	}
}

func (m *mockAlertRepo) ListAlerts(projectID int64) ([]repository.Alert, error) {
	var out []repository.Alert
	for _, a := range m.alerts {
		if a.ProjectID == projectID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *mockAlertRepo) QueryAggregates(f repository.AggregateFilter) ([]repository.Aggregate, error) {
	var out []repository.Aggregate
	for _, a := range m.aggregates {
		if f.ProjectID != 0 && a.ProjectID != f.ProjectID {
			continue
		}
		if f.Service != "" && a.Service != f.Service {
			continue
		}
		if f.Operation != "" && a.Operation != f.Operation {
			continue
		}
		if !f.From.IsZero() && a.Bucket.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && a.Bucket.After(f.To) {
			continue
		}
		out = append(out, a)
	}
	// Sort descending by bucket (simple bubble sort for test).
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Bucket.After(out[i].Bucket) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *mockAlertRepo) QueryMetricRollups(_ context.Context, f repository.MetricRollupFilter) ([]repository.MetricRollup, error) {
	var out []repository.MetricRollup
	for _, r := range m.rollups {
		if f.ProjectID != 0 && r.ProjectID != f.ProjectID {
			continue
		}
		if f.Name != "" && r.Name != f.Name {
			continue
		}
		if !f.From.IsZero() && r.Bucket.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && r.Bucket.After(f.To) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *mockAlertRepo) UpdateAlertLastTriggered(alertID int64, at time.Time) error {
	m.triggered[alertID] = at
	return nil
}

func TestEvaluateMetricThreshold(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	repo := newMockRepo()
	repo.alerts = []repository.Alert{{
		ID: 1, ProjectID: 1, Type: "metric_threshold",
		MetricName: "queue.depth", MetricAgg: "last", Threshold: 100,
		ComparisonWindow: 10, CooldownMinutes: 30, Enabled: true,
		WebhookURL: "https://hook", LabelFilters: "{}",
	}}
	// Flat baseline ~20, then a jump to 250 in the latest bucket.
	vals := []float64{20, 20, 20, 250}
	for i, v := range vals {
		repo.rollups = append(repo.rollups, repository.MetricRollup{
			ProjectID: 1, Name: "queue.depth", Type: "gauge", AttrFingerprint: "fp",
			Bucket: now.Add(time.Duration(i-4) * time.Minute), Count: 1, Sum: v, Min: v, Max: v, Last: v,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	if err := eval.Evaluate(context.Background(), 1); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(notifier.webhooks) != 1 {
		t.Fatalf("expected 1 webhook for breaching metric alert, got %d", len(notifier.webhooks))
	}
	if _, ok := repo.triggered[1]; !ok {
		t.Error("expected alert 1 to be marked triggered")
	}
}

func TestEvaluateMetricThresholdBelow(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	repo := newMockRepo()
	repo.alerts = []repository.Alert{{
		ID: 2, ProjectID: 1, Type: "metric_threshold",
		MetricName: "queue.depth", MetricAgg: "last", Threshold: 100,
		ComparisonWindow: 10, CooldownMinutes: 30, Enabled: true,
		WebhookURL: "https://hook", LabelFilters: "{}",
	}}
	for i, v := range []float64{20, 22, 19, 21} {
		repo.rollups = append(repo.rollups, repository.MetricRollup{
			ProjectID: 1, Name: "queue.depth", Type: "gauge", AttrFingerprint: "fp",
			Bucket: now.Add(time.Duration(i-4) * time.Minute), Count: 1, Sum: v, Last: v,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	if err := eval.Evaluate(context.Background(), 1); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(notifier.webhooks) != 0 {
		t.Errorf("expected no webhook below threshold, got %d", len(notifier.webhooks))
	}
}

func TestEvaluateNoAlerts(t *testing.T) {
	repo := newMockRepo()
	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.webhooks) != 0 || len(notifier.emails) != 0 {
		t.Fatal("expected no notifications")
	}
}

func TestEvaluateLatencyAlert(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)
	repo := newMockRepo()

	repo.alerts = []repository.Alert{
		{
			ID:               1,
			ProjectID:        1,
			Service:          "web",
			Operation:        "GET /api",
			Type:             "latency",
			Threshold:        100.0, // 100ms
			ComparisonWindow: 5,
			CooldownMinutes:  30,
			WebhookURL:       "https://hooks.example.com/alert",
			Enabled:          true,
		},
	}

	// Current bucket: p95 = 200ms = 200000us
	repo.aggregates = append(repo.aggregates, repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /api",
		Bucket: now.Add(-1 * time.Minute), Count: 100, P95Us: 200000,
	})

	// History: average p95 = 50ms = 50000us
	for i := 2; i <= 6; i++ {
		repo.aggregates = append(repo.aggregates, repository.Aggregate{
			ProjectID: 1, Service: "web", Operation: "GET /api",
			Bucket: now.Add(-time.Duration(i) * time.Minute), Count: 100, P95Us: 50000,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(notifier.webhooks))
	}

	wh := notifier.webhooks[0]
	if wh.url != "https://hooks.example.com/alert" {
		t.Fatalf("wrong webhook URL: %s", wh.url)
	}
	if wh.payload.Current != 200.0 {
		t.Fatalf("expected current=200.0, got %f", wh.payload.Current)
	}
	if wh.payload.Type != "latency" {
		t.Fatalf("expected type=latency, got %s", wh.payload.Type)
	}

	// Verify last_triggered_at was updated.
	if _, ok := repo.triggered[1]; !ok {
		t.Fatal("expected last_triggered_at to be updated")
	}
}

func TestEvaluateErrorRateAlert(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)
	repo := newMockRepo()

	repo.alerts = []repository.Alert{
		{
			ID:               2,
			ProjectID:        1,
			Service:          "api",
			Operation:        "POST /submit",
			Type:             "error_rate",
			Threshold:        5.0, // 5%
			ComparisonWindow: 3,
			CooldownMinutes:  15,
			Email:            "ops@example.com",
			Enabled:          true,
		},
	}

	// Current bucket: 20 errors out of 100 = 20%
	repo.aggregates = append(repo.aggregates, repository.Aggregate{
		ProjectID: 1, Service: "api", Operation: "POST /submit",
		Bucket: now.Add(-1 * time.Minute), Count: 100, ErrorCount: 20,
	})

	// History: average ~2%
	for i := 2; i <= 4; i++ {
		repo.aggregates = append(repo.aggregates, repository.Aggregate{
			ProjectID: 1, Service: "api", Operation: "POST /submit",
			Bucket: now.Add(-time.Duration(i) * time.Minute), Count: 100, ErrorCount: 2,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.emails) != 1 {
		t.Fatalf("expected 1 email, got %d", len(notifier.emails))
	}
	if notifier.emails[0].to != "ops@example.com" {
		t.Fatalf("wrong email to: %s", notifier.emails[0].to)
	}
}

func TestEvaluateCooldown(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)
	repo := newMockRepo()

	repo.alerts = []repository.Alert{
		{
			ID:               1,
			ProjectID:        1,
			Service:          "web",
			Operation:        "GET /api",
			Type:             "latency",
			Threshold:        100.0,
			ComparisonWindow: 5,
			CooldownMinutes:  30,
			WebhookURL:       "https://hooks.example.com/alert",
			Enabled:          true,
			LastTriggeredAt:  sql.NullTime{Time: now.Add(-10 * time.Minute), Valid: true}, // 10 min ago
		},
	}

	// Would trigger if not in cooldown.
	repo.aggregates = append(repo.aggregates, repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /api",
		Bucket: now.Add(-1 * time.Minute), Count: 100, P95Us: 200000,
	})
	for i := 2; i <= 6; i++ {
		repo.aggregates = append(repo.aggregates, repository.Aggregate{
			ProjectID: 1, Service: "web", Operation: "GET /api",
			Bucket: now.Add(-time.Duration(i) * time.Minute), Count: 100, P95Us: 50000,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.webhooks) != 0 {
		t.Fatal("expected no notifications due to cooldown")
	}
}

func TestEvaluateNoRegression(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)
	repo := newMockRepo()

	repo.alerts = []repository.Alert{
		{
			ID:               1,
			ProjectID:        1,
			Service:          "web",
			Operation:        "GET /api",
			Type:             "latency",
			Threshold:        50.0,
			ComparisonWindow: 5,
			CooldownMinutes:  30,
			WebhookURL:       "https://hooks.example.com/alert",
			Enabled:          true,
		},
	}

	// Current: 55ms (above threshold)
	repo.aggregates = append(repo.aggregates, repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /api",
		Bucket: now.Add(-1 * time.Minute), Count: 100, P95Us: 55000,
	})

	// History: average 50ms — only 10% increase, not enough (needs >20%)
	for i := 2; i <= 6; i++ {
		repo.aggregates = append(repo.aggregates, repository.Aggregate{
			ProjectID: 1, Service: "web", Operation: "GET /api",
			Bucket: now.Add(-time.Duration(i) * time.Minute), Count: 100, P95Us: 50000,
		})
	}

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.webhooks) != 0 {
		t.Fatal("expected no notification for small regression")
	}
}

func TestEvaluateBelowThreshold(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 10, 0, 0, time.UTC)
	repo := newMockRepo()

	repo.alerts = []repository.Alert{
		{
			ID:               1,
			ProjectID:        1,
			Service:          "web",
			Operation:        "GET /api",
			Type:             "latency",
			Threshold:        200.0, // 200ms threshold
			ComparisonWindow: 5,
			CooldownMinutes:  30,
			WebhookURL:       "https://hooks.example.com/alert",
			Enabled:          true,
		},
	}

	// Current: 100ms — below 200ms threshold.
	repo.aggregates = append(repo.aggregates, repository.Aggregate{
		ProjectID: 1, Service: "web", Operation: "GET /api",
		Bucket: now.Add(-1 * time.Minute), Count: 100, P95Us: 100000,
	})

	notifier := &mockNotifier{}
	eval := NewEvaluator(repo, notifier, slog.Default(), nil)
	eval.now = func() time.Time { return now }

	err := eval.Evaluate(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(notifier.webhooks) != 0 {
		t.Fatal("expected no notification when below threshold")
	}
}
