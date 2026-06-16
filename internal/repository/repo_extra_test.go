package repository

import (
	"context"
	"testing"
	"time"
)

func TestDB(t *testing.T) {
	repo := setupTestDB(t)
	if repo.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}

func TestSettings(t *testing.T) {
	repo := setupTestDB(t)

	// Get non-existent returns empty.
	val, err := repo.GetSetting("missing")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "" {
		t.Fatalf("expected empty, got %q", val)
	}

	// Set and get.
	if err := repo.SetSetting("key1", "val1"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	val, err = repo.GetSetting("key1")
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if val != "val1" {
		t.Fatalf("expected val1, got %q", val)
	}

	// Upsert.
	if err := repo.SetSetting("key1", "val2"); err != nil {
		t.Fatalf("SetSetting upsert: %v", err)
	}
	val, _ = repo.GetSetting("key1")
	if val != "val2" {
		t.Fatalf("expected val2 after upsert, got %q", val)
	}

	// GetAllSettings.
	_ = repo.SetSetting("key2", "val3")
	all, err := repo.GetAllSettings()
	if err != nil {
		t.Fatalf("GetAllSettings: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 settings, got %d", len(all))
	}
	if all["key1"] != "val2" || all["key2"] != "val3" {
		t.Fatalf("unexpected settings: %v", all)
	}
}

func TestGetDBSizeAndCounts(t *testing.T) {
	repo := setupTestDB(t)

	size, err := repo.GetDBSize(":memory:", "")
	if err != nil {
		t.Fatalf("GetDBSize: %v", err)
	}
	if size == nil {
		t.Fatal("size is nil")
	}

	counts, err := repo.GetDBCounts()
	if err != nil {
		t.Fatalf("GetDBCounts: %v", err)
	}
	if counts.SpanCount != 0 || counts.AggregateCount != 0 || counts.ErrorSampleCount != 0 {
		t.Fatalf("expected zero counts, got %+v", counts)
	}
}

func TestListProjectIDs(t *testing.T) {
	repo := setupTestDB(t)

	ids, err := repo.ListProjectIDs()
	if err != nil {
		t.Fatalf("ListProjectIDs empty: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected 0, got %d", len(ids))
	}

	p1, _ := repo.CreateProject("a", "A")
	p2, _ := repo.CreateProject("b", "B")

	ids, err = repo.ListProjectIDs()
	if err != nil {
		t.Fatalf("ListProjectIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2, got %d", len(ids))
	}
	found := map[int64]bool{p1.ID: false, p2.ID: false}
	for _, id := range ids {
		found[id] = true
	}
	for id, ok := range found {
		if !ok {
			t.Fatalf("project %d not found", id)
		}
	}
}

func TestEnsureSetupAPIKey(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	if err := repo.EnsureSetupAPIKey(p.ID, "abc123hash"); err != nil {
		t.Fatalf("EnsureSetupAPIKey: %v", err)
	}

	// Idempotent.
	if err := repo.EnsureSetupAPIKey(p.ID, "abc123hash"); err != nil {
		t.Fatalf("EnsureSetupAPIKey idempotent: %v", err)
	}

	k, err := repo.GetAPIKeyByHash("abc123hash")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if k.ProjectID != p.ID {
		t.Fatalf("expected projectID %d, got %d", p.ID, k.ProjectID)
	}
}

func TestListAllAPIKeys(t *testing.T) {
	repo := setupTestDB(t)
	p1, _ := repo.CreateProject("a", "A")
	p2, _ := repo.CreateProject("b", "B")
	_, _ = repo.CreateAPIKey(p1.ID, "k1", "h1", "ingest")
	_, _ = repo.CreateAPIKey(p2.ID, "k2", "h2", "read")

	all, err := repo.ListAllAPIKeys()
	if err != nil {
		t.Fatalf("ListAllAPIKeys: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}
}

func TestUpdateAlert(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	id, err := repo.CreateAlert(Alert{
		ProjectID: p.ID,
		Service:   "web",
		Operation: "GET /",
		Type:      "latency_p95",
		Threshold: 1000,
	})
	if err != nil {
		t.Fatalf("CreateAlert: %v", err)
	}

	err = repo.UpdateAlert(Alert{
		ID:        id,
		ProjectID: p.ID,
		Service:   "web",
		Operation: "GET /",
		Type:      "error_rate",
		Threshold: 0.05,
	})
	if err != nil {
		t.Fatalf("UpdateAlert: %v", err)
	}

	alerts, _ := repo.ListAlerts(p.ID)
	if len(alerts) != 1 {
		t.Fatalf("expected 1, got %d", len(alerts))
	}
	if alerts[0].Type != "error_rate" || alerts[0].Threshold != 0.05 {
		t.Fatalf("unexpected alert: %+v", alerts[0])
	}
}

func TestGetSpansBySpanIDs(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	_ = repo.InsertSpans([]Span{
		makeSpan(p.ID, "t1", "s1", "web", "op1", "ok", 100),
		makeSpan(p.ID, "t1", "s2", "web", "op2", "ok", 200),
		makeSpan(p.ID, "t1", "s3", "web", "op3", "ok", 300),
	})

	spans, err := repo.GetSpansBySpanIDs([]string{"s1", "s3"})
	if err != nil {
		t.Fatalf("GetSpansBySpanIDs: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("expected 2, got %d", len(spans))
	}

	// Empty input.
	spans, err = repo.GetSpansBySpanIDs(nil)
	if err != nil {
		t.Fatalf("GetSpansBySpanIDs empty: %v", err)
	}
	if len(spans) != 0 {
		t.Fatalf("expected 0, got %d", len(spans))
	}
}

func TestDeleteSpansByIDs(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	_ = repo.InsertSpans([]Span{
		makeSpan(p.ID, "t1", "s1", "web", "op", "ok", 100),
		makeSpan(p.ID, "t1", "s2", "web", "op", "ok", 200),
	})

	all, _ := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Limit: 10})
	ids := []int64{all[0].ID}

	n, err := repo.DeleteSpansByIDs(ids)
	if err != nil {
		t.Fatalf("DeleteSpansByIDs: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}

	remaining, _ := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Limit: 10})
	if len(remaining) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(remaining))
	}

	// Empty input.
	n, err = repo.DeleteSpansByIDs(nil)
	if err != nil {
		t.Fatalf("DeleteSpansByIDs empty: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestQueryServiceStatsFromSpans(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	_ = repo.InsertSpans([]Span{
		makeSpan(p.ID, "t1", "s1", "web", "GET /", "ok", 1000),
		makeSpan(p.ID, "t1", "s2", "web", "GET /", "error", 5000),
		makeSpan(p.ID, "t2", "s3", "worker", "job", "ok", 200),
	})

	stats, err := repo.QueryServiceStatsFromSpans(p.ID, time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("QueryServiceStatsFromSpans: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("expected 2 services, got %d", len(stats))
	}
}

func TestPromptRecords(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	temp := 0.7
	records := []PromptRecord{
		{
			ProjectID:    p.ID,
			TraceID:      "t1",
			SpanID:       "sp1",
			Service:      "api",
			Name:         "chat",
			GenAISystem:  "openai",
			Model:        "gpt-4",
			Temperature:  &temp,
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
			CostUSD:      0.01,
			DurationUs:   5000,
			Status:       "ok",
			FinishReason: "stop",
			StartTimeUs:  time.Now().UnixMicro(),
		},
		{
			ProjectID:    p.ID,
			TraceID:      "t2",
			SpanID:       "sp2",
			Service:      "worker",
			Name:         "summarize",
			GenAISystem:  "anthropic",
			Model:        "claude-3",
			InputTokens:  200,
			OutputTokens: 100,
			TotalTokens:  300,
			CostUSD:      0.02,
			DurationUs:   10000,
			Status:       "error",
			FinishReason: "error",
			StartTimeUs:  time.Now().UnixMicro(),
		},
	}

	// Insert.
	if err := repo.InsertPromptRecords(records); err != nil {
		t.Fatalf("InsertPromptRecords: %v", err)
	}

	// Empty insert.
	if err := repo.InsertPromptRecords(nil); err != nil {
		t.Fatalf("InsertPromptRecords empty: %v", err)
	}

	// Query all.
	all, err := repo.QueryPromptRecords(PromptFilter{ProjectID: p.ID, Limit: 10})
	if err != nil {
		t.Fatalf("QueryPromptRecords: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}

	// Filter by service.
	svc, err := repo.QueryPromptRecords(PromptFilter{ProjectID: p.ID, Service: "api", Limit: 10})
	if err != nil {
		t.Fatalf("QueryPromptRecords service: %v", err)
	}
	if len(svc) != 1 {
		t.Fatalf("expected 1, got %d", len(svc))
	}

	// Filter by model.
	mdl, err := repo.QueryPromptRecords(PromptFilter{ProjectID: p.ID, Model: "gpt-4", Limit: 10})
	if err != nil {
		t.Fatalf("QueryPromptRecords model: %v", err)
	}
	if len(mdl) != 1 {
		t.Fatalf("expected 1, got %d", len(mdl))
	}

	// Filter by status.
	errs, err := repo.QueryPromptRecords(PromptFilter{ProjectID: p.ID, Status: "error", Limit: 10})
	if err != nil {
		t.Fatalf("QueryPromptRecords status: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1, got %d", len(errs))
	}

	// GetPromptRecordsByTraceID.
	byTrace, err := repo.GetPromptRecordsByTraceID("t1")
	if err != nil {
		t.Fatalf("GetPromptRecordsByTraceID: %v", err)
	}
	if len(byTrace) != 1 {
		t.Fatalf("expected 1, got %d", len(byTrace))
	}

	// Delete with future cutoff.
	n, err := repo.DeletePromptRecordsOlderThan(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DeletePromptRecordsOlderThan: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
}

func TestSearchTraceSummaries(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	// Three traces: error trace (2 spans), ok trace (1 span), health trace (1 span, excluded).
	root1 := makeSpan(p.ID, "trace-err", "span-r1", "web", "GET /slow", "error", 5000)
	child1 := makeSpan(p.ID, "trace-err", "span-c1", "web", "db.query", "ok", 1000)
	child1.ParentSpanID = "span-r1"
	root2 := makeSpan(p.ID, "trace-ok", "span-r2", "api", "POST /create", "ok", 500)
	rootHealth := makeSpan(p.ID, "trace-health", "span-h1", "probe", "GET /health", "ok", 1)

	_ = repo.InsertSpans([]Span{root1, child1, root2, rootHealth})

	rows, err := repo.SearchTraceSummaries(SpanFilter{ProjectID: p.ID, Limit: 10}, 0)
	if err != nil {
		t.Fatalf("SearchTraceSummaries: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 traces, got %d", len(rows))
	}

	// Errors-first sort.
	rows, err = repo.SearchTraceSummaries(SpanFilter{
		ProjectID: p.ID, Limit: 10, SortErrorsFirst: true,
	}, 0)
	if err != nil {
		t.Fatalf("SearchTraceSummaries SortErrorsFirst: %v", err)
	}
	if len(rows) == 0 || !rows[0].HasError {
		t.Fatalf("expected error trace first")
	}

	// RootOnly filter: all 3 traces have root spans.
	rows, err = repo.SearchTraceSummaries(SpanFilter{
		ProjectID: p.ID, Limit: 10, RootOnly: true,
	}, 0)
	if err != nil {
		t.Fatalf("SearchTraceSummaries RootOnly: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 root traces, got %d", len(rows))
	}

	// ExcludeOperations removes the single-span health trace.
	rows, err = repo.SearchTraceSummaries(SpanFilter{
		ProjectID: p.ID, Limit: 10, ExcludeOperations: []string{"GET /health"},
	}, 0)
	if err != nil {
		t.Fatalf("SearchTraceSummaries ExcludeOperations: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 traces after health exclusion, got %d", len(rows))
	}
}

func TestQueryRootSpanGroups(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	root1 := makeSpan(p.ID, "t1", "s1", "web", "GET /", "ok", 1000)
	root2 := makeSpan(p.ID, "t2", "s2", "web", "GET /", "error", 2000)
	root3 := makeSpan(p.ID, "t3", "s3", "api", "POST /users", "ok", 500)
	child := makeSpan(p.ID, "t1", "s4", "web", "db.query", "ok", 100)
	child.ParentSpanID = "s1"

	_ = repo.InsertSpans([]Span{root1, root2, root3, child})

	groups, err := repo.QueryRootSpanGroups(context.Background(), SpanFilter{
		ProjectID: p.ID,
	})
	if err != nil {
		t.Fatalf("QueryRootSpanGroups: %v", err)
	}
	// Only root spans (no parent): root1, root2, root3 — child excluded.
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (GET / and POST /users), got %d", len(groups))
	}

	// Find GET / group — should have 2 entries (root1 + root2) and 1 error.
	var getGroup *RootSpanGroup
	for i := range groups {
		if groups[i].Operation == "GET /" {
			getGroup = &groups[i]
		}
	}
	if getGroup == nil {
		t.Fatal("GET / group not found")
	}
	if getGroup.Count != 2 {
		t.Fatalf("expected count 2, got %d", getGroup.Count)
	}
	if getGroup.ErrorCount != 1 {
		t.Fatalf("expected 1 error, got %d", getGroup.ErrorCount)
	}
	if len(getGroup.Durations) != 2 {
		t.Fatalf("expected 2 durations, got %d", len(getGroup.Durations))
	}

	// ExcludeOperations.
	groups, err = repo.QueryRootSpanGroups(context.Background(), SpanFilter{
		ProjectID: p.ID, ExcludeOperations: []string{"GET /"},
	})
	if err != nil {
		t.Fatalf("QueryRootSpanGroups exclude: %v", err)
	}
	if len(groups) != 1 || groups[0].Operation != "POST /users" {
		t.Fatalf("expected only POST /users, got %v", groups)
	}
}

func TestTraceExclusions(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	// Empty.
	excls, err := repo.ListTraceExclusions(p.ID)
	if err != nil {
		t.Fatalf("ListTraceExclusions empty: %v", err)
	}
	if len(excls) != 0 {
		t.Fatalf("expected 0, got %d", len(excls))
	}

	// Create.
	id1, err := repo.CreateTraceExclusion(p.ID, "GET /health")
	if err != nil {
		t.Fatalf("CreateTraceExclusion: %v", err)
	}
	if id1 == 0 {
		t.Fatal("expected non-zero ID")
	}
	_, _ = repo.CreateTraceExclusion(p.ID, "GET /metrics")

	excls, _ = repo.ListTraceExclusions(p.ID)
	if len(excls) != 2 {
		t.Fatalf("expected 2, got %d", len(excls))
	}

	// ExcludedOperations.
	ops, err := repo.ExcludedOperations(p.ID)
	if err != nil {
		t.Fatalf("ExcludedOperations: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ops))
	}

	// Delete.
	if err := repo.DeleteTraceExclusion(id1); err != nil {
		t.Fatalf("DeleteTraceExclusion: %v", err)
	}
	excls, _ = repo.ListTraceExclusions(p.ID)
	if len(excls) != 1 {
		t.Fatalf("expected 1 after delete, got %d", len(excls))
	}
}
