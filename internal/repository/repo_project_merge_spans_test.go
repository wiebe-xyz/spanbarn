package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func spansExec(t *testing.T, repo *Repository, q string, args ...any) {
	t.Helper()
	if err := repo.execHigh(engine.FamilySpans, func(db *sql.DB) error {
		_, err := db.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func spansCount(t *testing.T, repo *Repository, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := repo.reader(engine.FamilySpans).QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func spansText(t *testing.T, repo *Repository, q string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := repo.reader(engine.FamilySpans).QueryRow(q, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return s.String
}

var mergeAt = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// spansMergeFixture holds spans, summaries, aggregates and both rollup tiers in
// the source project, with rows in the target that share keys with some of them.
func spansMergeFixture(t *testing.T) mergeFixture {
	t.Helper()
	f := newMergeFixture(t)
	repo, src, tgt := f.repo, f.source.ID, f.target.ID

	mk := func(project int64, id, attrs string) Span {
		return Span{ProjectID: project, TraceID: "t-" + id, SpanID: id, Name: "GET /", Service: "web", Kind: "server",
			Status: "ok", StartTimeUs: mergeAt.UnixMicro(), DurationUs: 100, Attributes: attrs, Events: "[]"}
	}
	if err := repo.InsertSpans([]Span{
		mk(src, "a", `{"x":1}`),
		mk(src, "b", `{"deployment.environment":"qa"}`),
		mk(src, "c", `{}`),
		mk(src, "d", `not json`),
		mk(tgt, "z", `{"deployment.environment":"production"}`),
	}); err != nil {
		t.Fatal(err)
	}

	for _, p := range []struct {
		project           int64
		trace             string
		spans, errs, root int
		name              string
		start             int64
		durable           int
		orphans           int
	}{
		{src, "shared", 3, 1, 0, "", 500, 0, 3},
		{tgt, "shared", 4, 0, 1, "GET /root", 400, 1, 0},
		{src, "only-src", 2, 0, 1, "GET /x", 100, 0, 0},
	} {
		spansExec(t, repo, `INSERT INTO trace_summaries (project_id, trace_id, root_name, root_service, root_duration_us, start_time_us,
			span_count, has_error, ingested_at, has_root, orphan_count, durable) VALUES (?, ?, ?, 'web', 9, ?, ?, ?, '2026-11-10 12:00:00', ?, ?, ?)`,
			p.project, p.trace, p.name, p.start, p.spans, p.errs, p.root, p.orphans, p.durable)
	}
	spansExec(t, repo, `INSERT INTO error_exemplars (project_id, service, name, day, trace_id) VALUES (?, 'web', 'GET /', '2026-11-10', 'src-trace'), (?, 'web', 'GET /', '2026-11-10', 'tgt-trace'), (?, 'web', 'GET /y', '2026-11-10', 'src-only')`, src, tgt, src)
	spansExec(t, repo, `INSERT INTO spans_staging (project_id, trace_id, span_id, name, service, start_time_us, duration_us, attributes) VALUES (?, 'ts', 's1', 'n', 'web', 1, 1, '{}')`, src)
	spansExec(t, repo, `INSERT INTO error_samples (project_id, trace_id, span_id, name, service, start_time_us, duration_us, attributes) VALUES (?, 'te', 'e1', 'n', 'web', 1, 1, '{}')`, src)

	agg := func(project int64, env string, n int64) Aggregate {
		return Aggregate{ProjectID: project, Environment: env, Service: "web", Operation: "GET /", Kind: "server",
			Bucket: mergeAt, Count: n, ErrorCount: 1, MaxUs: n * 10, SumDurationUs: n * 100, WeightedCount: n * 2, WeightedSpans: n}
	}
	if err := repo.UpsertAggregates([]Aggregate{agg(src, "", 3), agg(src, "qa", 5), agg(tgt, "staging", 4)}); err != nil {
		t.Fatal(err)
	}

	b := mergeAt.Truncate(5 * time.Minute)
	r1, r2, r3 := spanRollupRow(b, "GET /", 10, 20), spanRollupRow(b, "GET /", 30), spanRollupRow(b, "GET /other", 5)
	r1.ProjectID, r1.Environment = src, ""
	r2.ProjectID, r2.Environment = tgt, "staging"
	r3.ProjectID, r3.Environment = src, "qa"
	if err := repo.AddSpanRollups([]SpanRollup{r1, r2, r3}); err != nil {
		t.Fatal(err)
	}
	e1, e2 := spanEdgeRow(b, src, "api", "sqlite", 10, 20), spanEdgeRow(b, tgt, "api", "sqlite", 40)
	e2.Environment = "staging"
	if err := repo.AddSpanEdges([]SpanEdge{e1, e2}); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMergeProjectMovesSpansFamily(t *testing.T) {
	f := spansMergeFixture(t)
	results, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2})
	if err != nil {
		t.Fatalf("MergeProject: %v", err)
	}
	moved, collided := map[string]int64{}, map[string]int64{}
	for _, r := range results {
		moved[r.Table] += r.Moved
		collided[r.Table] += r.Collided
	}
	for table, want := range map[string]int64{"spans": 4, "error_samples": 1, "trace_summaries": 6, "error_exemplars": 2,
		"aggregates": 2, "span_rollups": 2, "span_edges": 1, "spans_staging": 1} {
		if moved[table] != want {
			t.Errorf("%s moved %d, want %d", table, moved[table], want)
		}
	}
	for table, want := range map[string]int64{"trace_summaries": 1, "error_exemplars": 1, "span_rollups": 1, "span_edges": 1} {
		if collided[table] != want {
			t.Errorf("%s collided %d, want %d", table, collided[table], want)
		}
	}
	src, tgt := f.source.ID, f.target.ID
	for _, table := range []string{"spans", "error_samples", "trace_summaries", "error_exemplars", "aggregates", "span_rollups", "span_edges", "spans_staging"} {
		if n := spansCount(t, f.repo, `SELECT count(*) FROM `+table+` WHERE project_id = ?`, src); n != 0 {
			t.Errorf("%s keeps %d source rows", table, n)
		}
	}
	if n := spansCount(t, f.repo, `SELECT count(*) FROM spans WHERE project_id = ?`, tgt); n != 5 {
		t.Errorf("target spans = %d, want 5", n)
	}
}

func TestMergeProjectStampsSpanEnvironment(t *testing.T) {
	f := spansMergeFixture(t)
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	env := func(id string) string {
		return spansText(t, f.repo, `SELECT json_extract(attributes, '$."deployment.environment"') FROM spans WHERE span_id = ?`, id)
	}
	if env("a") != "staging" || env("c") != "staging" {
		t.Errorf("untagged spans read %q and %q, want staging", env("a"), env("c"))
	}
	if env("b") != "qa" {
		t.Errorf("a span with its own environment reads %q, want qa", env("b"))
	}
	if got := spansText(t, f.repo, `SELECT attributes FROM spans WHERE span_id = 'd'`); got != "not json" {
		t.Errorf("an attribute blob that is not JSON changed to %q", got)
	}
	if got := spansText(t, f.repo, `SELECT json_extract(attributes, '$.x') FROM spans WHERE span_id = 'a'`); got != "1" {
		t.Errorf("stamping dropped the other attributes: x = %q", got)
	}
	// The untagged source row takes the staging key the target already holds.
	if n := spansCount(t, f.repo, `SELECT count(*) FROM aggregates WHERE project_id = ? AND environment = 'staging'`, f.target.ID); n != 1 {
		t.Errorf("aggregates in staging = %d, want one folded row", n)
	}
	if n := spansCount(t, f.repo, `SELECT count(*) FROM aggregates WHERE project_id = ? AND environment = ''`, f.target.ID); n != 0 {
		t.Errorf("target keeps %d untagged aggregate rows", n)
	}
}

func TestMergeProjectLeavesProductionSpansUnstamped(t *testing.T) {
	f := spansMergeFixture(t)
	spec := f.spec()
	spec.Environment = "production"
	if _, err := f.repo.MergeProject(context.Background(), spec, MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := spansText(t, f.repo, `SELECT attributes FROM spans WHERE span_id = 'a'`); got != `{"x":1}` {
		t.Errorf("a production merge rewrote attributes to %q", got)
	}
}

func TestMergeProjectFoldsCollidingRows(t *testing.T) {
	f := spansMergeFixture(t)
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 1}); err != nil {
		t.Fatal(err)
	}
	tgt := f.target.ID

	var spanCount, orphans, hasError, hasRoot, durable, start int64
	var rootName string
	if err := f.repo.reader(engine.FamilySpans).QueryRow(`SELECT span_count, orphan_count, has_error, has_root, durable, start_time_us, root_name
		FROM trace_summaries WHERE project_id = ? AND trace_id = 'shared'`, tgt).Scan(&spanCount, &orphans, &hasError, &hasRoot, &durable, &start, &rootName); err != nil {
		t.Fatal(err)
	}
	if spanCount != 7 || orphans != 3 || hasError != 1 || hasRoot != 1 || durable != 1 || start != 400 || rootName != "GET /root" {
		t.Errorf("folded summary = spans %d orphans %d error %d root %d durable %d start %d name %q", spanCount, orphans, hasError, hasRoot, durable, start, rootName)
	}

	if got := spansText(t, f.repo, `SELECT trace_id FROM error_exemplars WHERE project_id = ? AND name = 'GET /'`, tgt); got != "tgt-trace" {
		t.Errorf("exemplar = %q, want the target's own", got)
	}

	// The source untagged aggregate lands in staging beside the target's row of
	// the same key: it adds, and the qa row keeps its environment.
	if n := spansCount(t, f.repo, `SELECT count FROM aggregates WHERE project_id = ? AND environment = 'staging'`, tgt); n != 7 {
		t.Errorf("staging aggregate count = %d, want 3 + 4", n)
	}
	if n := spansCount(t, f.repo, `SELECT count FROM aggregates WHERE project_id = ? AND environment = 'qa'`, tgt); n != 5 {
		t.Errorf("qa aggregate count = %d, want 5", n)
	}

	var count, sum, minUs, maxUs int64
	if err := f.repo.reader(engine.FamilySpans).QueryRow(`SELECT count, sum_duration_us, min_us, max_us FROM span_rollups
		WHERE project_id = ? AND environment = 'staging' AND name = 'GET /'`, tgt).Scan(&count, &sum, &minUs, &maxUs); err != nil {
		t.Fatal(err)
	}
	if count != 3 || sum != 60 || minUs != 10 || maxUs != 30 {
		t.Errorf("folded rollup = count %d sum %d min %d max %d", count, sum, minUs, maxUs)
	}
	if n := spansCount(t, f.repo, `SELECT count FROM span_edges WHERE project_id = ? AND environment = 'staging' AND source = 'api'`, tgt); n != 3 {
		t.Errorf("folded edge count = %d, want 3", n)
	}
}

func TestMergeProjectResumesSpansBatches(t *testing.T) {
	f := spansMergeFixture(t)
	stop := errors.New("stop")
	batches := 0
	_, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 1, OnBatch: func(MergeStepResult) error {
		batches++
		if batches == 12 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatalf("first run = %v, want the stop error", err)
	}
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 1}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	for _, table := range []string{"spans", "trace_summaries", "aggregates", "span_rollups", "span_edges"} {
		if n := spansCount(t, f.repo, `SELECT count(*) FROM `+table+` WHERE project_id = ?`, f.source.ID); n != 0 {
			t.Errorf("%s keeps %d source rows after the resume", table, n)
		}
	}
	// A summary that collided before the stop folds once, whatever the batch size.
	if n := spansCount(t, f.repo, `SELECT span_count FROM trace_summaries WHERE project_id = ? AND trace_id = 'shared'`, f.target.ID); n != 7 {
		t.Errorf("shared summary span_count = %d, want 7", n)
	}
	if n := spansCount(t, f.repo, `SELECT count FROM aggregates WHERE project_id = ? AND environment = 'staging'`, f.target.ID); n != 7 {
		t.Errorf("staging aggregate count = %d, want 7", n)
	}
}
