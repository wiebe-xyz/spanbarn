package repository

import (
	"database/sql"
	"testing"
	"time"
)

// setupTestDB creates an in-memory SQLite database with migrations applied.
func setupTestDB(t *testing.T) *Repository {
	t.Helper()
	db, err := NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRepository(db.DB)
}

func TestCreateAndGetProject(t *testing.T) {
	repo := setupTestDB(t)

	p, err := repo.CreateProject("my-app", "My App")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug != "my-app" || p.Name != "My App" || p.ID == 0 {
		t.Fatalf("unexpected project: %+v", p)
	}

	got, err := repo.GetProjectBySlug("my-app")
	if err != nil {
		t.Fatalf("GetProjectBySlug: %v", err)
	}
	if got.ID != p.ID || got.Slug != p.Slug {
		t.Fatalf("mismatch: got %+v, want %+v", got, p)
	}

	// Duplicate slug should fail.
	_, err = repo.CreateProject("my-app", "Duplicate")
	if err == nil {
		t.Fatal("expected error for duplicate slug")
	}

	// ListProjects
	_, _ = repo.CreateProject("second", "Second")
	list, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(list))
	}
}

func TestCreateAndGetUser(t *testing.T) {
	repo := setupTestDB(t)

	if err := repo.CreateUser("alice", "$2a$hash"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	u, err := repo.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.Username != "alice" || u.PasswordHash != "$2a$hash" {
		t.Fatalf("unexpected user: %+v", u)
	}

	// Duplicate username should fail.
	if err := repo.CreateUser("alice", "x"); err == nil {
		t.Fatal("expected error for duplicate username")
	}

	// DeleteUser
	if err := repo.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	_, err = repo.GetUserByUsername("alice")
	if err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows after delete, got %v", err)
	}

	// Delete nonexistent user.
	if err := repo.DeleteUser("nobody"); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows for nonexistent user, got %v", err)
	}

	// ListUsers
	_ = repo.CreateUser("bob", "hash1")
	_ = repo.CreateUser("carol", "hash2")
	list, err := repo.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 users, got %d", len(list))
	}
}

func TestUpdateUserPassword(t *testing.T) {
	repo := setupTestDB(t)

	_ = repo.CreateUser("admin", "oldhash")

	if err := repo.UpdateUserPassword("admin", "newhash"); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	u, err := repo.GetUserByUsername("admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if u.PasswordHash != "newhash" {
		t.Fatalf("expected newhash, got %q", u.PasswordHash)
	}

	// Non-existent user returns error.
	if err := repo.UpdateUserPassword("nobody", "x"); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows for nonexistent user, got %v", err)
	}
}

func TestCreateAndGetAPIKey(t *testing.T) {
	repo := setupTestDB(t)

	p, _ := repo.CreateProject("proj", "Proj")

	id, err := repo.CreateAPIKey(p.ID, "test-key", "sha256hash", "ingest")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero ID")
	}

	k, err := repo.GetAPIKeyByHash("sha256hash")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if k.Name != "test-key" || k.Scope != "ingest" || k.ProjectID != p.ID {
		t.Fatalf("unexpected key: %+v", k)
	}

	// ListAPIKeys
	_, _ = repo.CreateAPIKey(p.ID, "key2", "hash2", "read")
	list, err := repo.ListAPIKeys(p.ID)
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(list))
	}
}

func TestRevokeAPIKey(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	id, _ := repo.CreateAPIKey(p.ID, "k", "h", "ingest")

	if err := repo.RevokeAPIKey(id); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	_, err := repo.GetAPIKeyByHash("h")
	if err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows after revoke, got %v", err)
	}

	// Revoke nonexistent.
	if err := repo.RevokeAPIKey(9999); err != sql.ErrNoRows {
		t.Fatalf("expected ErrNoRows, got %v", err)
	}
}

func TestTouchAPIKey(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")
	_, _ = repo.CreateAPIKey(p.ID, "k", "h", "ingest")

	k1, _ := repo.GetAPIKeyByHash("h")
	if k1.LastUsedAt.Valid {
		t.Fatal("expected LastUsedAt to be NULL initially")
	}

	if err := repo.TouchAPIKey(k1.ID); err != nil {
		t.Fatalf("TouchAPIKey: %v", err)
	}

	k2, _ := repo.GetAPIKeyByHash("h")
	if !k2.LastUsedAt.Valid {
		t.Fatal("expected LastUsedAt to be set after touch")
	}
}

func makeSpan(projectID int64, traceID, spanID, service, name, status string, durationUs int64) Span {
	return Span{
		ProjectID:  projectID,
		TraceID:    traceID,
		SpanID:     spanID,
		Name:       name,
		Service:    service,
		Resource:   "/api/test",
		Kind:       "server",
		Status:     status,
		StartTimeUs: time.Now().UnixMicro(),
		DurationUs: durationUs,
		Attributes: `{"key":"value"}`,
		Events:     `[]`,
	}
}

func TestInsertAndQuerySpans(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	spans := []Span{
		makeSpan(p.ID, "trace1", "span1", "web", "GET /api", "ok", 1000),
		makeSpan(p.ID, "trace1", "span2", "web", "GET /api", "error", 5000),
		makeSpan(p.ID, "trace2", "span3", "worker", "process", "ok", 200),
	}

	if err := repo.InsertSpans(spans); err != nil {
		t.Fatalf("InsertSpans: %v", err)
	}

	// Query all for project.
	all, err := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(all))
	}

	// Filter by service.
	webSpans, err := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Service: "web", Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans service: %v", err)
	}
	if len(webSpans) != 2 {
		t.Fatalf("expected 2 web spans, got %d", len(webSpans))
	}

	// Filter by status.
	errSpans, err := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Status: "error", Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans status: %v", err)
	}
	if len(errSpans) != 1 {
		t.Fatalf("expected 1 error span, got %d", len(errSpans))
	}

	// Filter by operation.
	opSpans, err := repo.QuerySpans(SpanFilter{ProjectID: p.ID, Operation: "GET /api", Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans operation: %v", err)
	}
	if len(opSpans) != 2 {
		t.Fatalf("expected 2 spans for operation, got %d", len(opSpans))
	}

	// Filter by min duration.
	slowSpans, err := repo.QuerySpans(SpanFilter{ProjectID: p.ID, MinDuration: 2000, Limit: 10})
	if err != nil {
		t.Fatalf("QuerySpans minDuration: %v", err)
	}
	if len(slowSpans) != 1 {
		t.Fatalf("expected 1 slow span, got %d", len(slowSpans))
	}

	// Empty insert should be no-op.
	if err := repo.InsertSpans(nil); err != nil {
		t.Fatalf("InsertSpans empty: %v", err)
	}
}

func TestGetTraceByID(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	spans := []Span{
		makeSpan(p.ID, "trace-abc", "s1", "web", "GET /", "ok", 100),
		makeSpan(p.ID, "trace-abc", "s2", "web", "DB query", "ok", 50),
		makeSpan(p.ID, "trace-other", "s3", "web", "GET /other", "ok", 100),
	}
	_ = repo.InsertSpans(spans)

	trace, err := repo.GetTraceByID("trace-abc")
	if err != nil {
		t.Fatalf("GetTraceByID: %v", err)
	}
	if len(trace) != 2 {
		t.Fatalf("expected 2 spans in trace, got %d", len(trace))
	}
}

func TestDeleteSpansOlderThan(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	_ = repo.InsertSpans([]Span{
		makeSpan(p.ID, "t1", "s1", "web", "op", "ok", 100),
	})

	// Delete with future cutoff should remove everything.
	n, err := repo.DeleteSpansOlderThan(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteSpansOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}

	// Verify empty.
	all, _ := repo.QuerySpans(SpanFilter{ProjectID: p.ID})
	if len(all) != 0 {
		t.Fatalf("expected 0 spans after delete, got %d", len(all))
	}
}

func TestGetSpansForAggregation(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	_ = repo.InsertSpans([]Span{
		makeSpan(p.ID, "t1", "s1", "web", "op", "ok", 100),
		makeSpan(p.ID, "t1", "s2", "web", "op", "ok", 200),
	})

	spans, err := repo.GetSpansForAggregation(time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("GetSpansForAggregation: %v", err)
	}
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
}

func TestUpsertAggregate(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	agg := Aggregate{
		ProjectID:     p.ID,
		Service:       "web",
		Operation:     "GET /api",
		Resource:      "/api",
		Kind:          "server",
		Bucket:        bucket,
		Count:         10,
		ErrorCount:    2,
		P50Us:         500,
		P95Us:         1500,
		P99Us:         3000,
		MaxUs:         5000,
		SumDurationUs: 10000,
	}

	if err := repo.UpsertAggregate(agg); err != nil {
		t.Fatalf("UpsertAggregate: %v", err)
	}

	// Upsert again to test conflict resolution.
	agg2 := agg
	agg2.Count = 5
	agg2.ErrorCount = 1
	agg2.MaxUs = 3000 // lower than existing
	agg2.SumDurationUs = 5000
	if err := repo.UpsertAggregate(agg2); err != nil {
		t.Fatalf("UpsertAggregate second: %v", err)
	}

	results, err := repo.QueryAggregates(AggregateFilter{ProjectID: p.ID, Limit: 10})
	if err != nil {
		t.Fatalf("QueryAggregates: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(results))
	}

	r := results[0]
	if r.Count != 15 { // 10 + 5
		t.Fatalf("expected count=15, got %d", r.Count)
	}
	if r.ErrorCount != 3 { // 2 + 1
		t.Fatalf("expected error_count=3, got %d", r.ErrorCount)
	}
	if r.MaxUs != 5000 { // MAX(5000, 3000)
		t.Fatalf("expected max_us=5000, got %d", r.MaxUs)
	}
	if r.SumDurationUs != 15000 { // 10000 + 5000
		t.Fatalf("expected sum_duration_us=15000, got %d", r.SumDurationUs)
	}
}

func TestQueryAggregates(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	bucket1 := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	bucket2 := time.Date(2026, 5, 3, 13, 0, 0, 0, time.UTC)

	_ = repo.UpsertAggregate(Aggregate{ProjectID: p.ID, Service: "web", Operation: "GET /", Bucket: bucket1, Count: 1})
	_ = repo.UpsertAggregate(Aggregate{ProjectID: p.ID, Service: "worker", Operation: "job", Bucket: bucket2, Count: 2})

	// Filter by service.
	webAggs, err := repo.QueryAggregates(AggregateFilter{ProjectID: p.ID, Service: "web", Limit: 10})
	if err != nil {
		t.Fatalf("QueryAggregates service: %v", err)
	}
	if len(webAggs) != 1 {
		t.Fatalf("expected 1, got %d", len(webAggs))
	}

	// Filter by time range.
	rangeAggs, err := repo.QueryAggregates(AggregateFilter{
		ProjectID: p.ID,
		From:      bucket1.Add(-time.Minute),
		To:        bucket1.Add(time.Minute),
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("QueryAggregates range: %v", err)
	}
	if len(rangeAggs) != 1 {
		t.Fatalf("expected 1 in range, got %d", len(rangeAggs))
	}
}

func TestDeleteAggregatesOlderThan(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	bucket := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	_ = repo.UpsertAggregate(Aggregate{ProjectID: p.ID, Service: "web", Operation: "op", Bucket: bucket, Count: 1})

	n, err := repo.DeleteAggregatesOlderThan(bucket.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteAggregatesOlderThan: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 deleted, got %d", n)
	}
}

func TestInsertAndQueryErrorSamples(t *testing.T) {
	repo := setupTestDB(t)
	p, _ := repo.CreateProject("proj", "Proj")

	spans := []Span{
		makeSpan(p.ID, "trace1", "span1", "web", "GET /api", "error", 5000),
		makeSpan(p.ID, "trace2", "span2", "worker", "process", "error", 3000),
	}

	if err := repo.InsertErrorSamples(spans); err != nil {
		t.Fatalf("InsertErrorSamples: %v", err)
	}

	// Query all.
	all, err := repo.QueryErrorSamples(SpanFilter{ProjectID: p.ID, Limit: 10})
	if err != nil {
		t.Fatalf("QueryErrorSamples: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2, got %d", len(all))
	}

	// Filter by service.
	webErrs, err := repo.QueryErrorSamples(SpanFilter{ProjectID: p.ID, Service: "web", Limit: 10})
	if err != nil {
		t.Fatalf("QueryErrorSamples service: %v", err)
	}
	if len(webErrs) != 1 {
		t.Fatalf("expected 1 web error, got %d", len(webErrs))
	}

	// Delete with future cutoff.
	n, err := repo.DeleteErrorSamplesOlderThan(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteErrorSamplesOlderThan: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}

	// Empty insert should be no-op.
	if err := repo.InsertErrorSamples(nil); err != nil {
		t.Fatalf("InsertErrorSamples empty: %v", err)
	}
}
