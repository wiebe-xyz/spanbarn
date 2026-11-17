package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func labelsJSON(labels map[string]string) string { return metrics.CanonicalAttributes(labels) }

func rollupRow(project int64, labels map[string]string, bucket time.Time, count int64, extra string) MetricRollup {
	return MetricRollup{
		ProjectID: project, Name: "req", Type: "sum", AttrFingerprint: metrics.Fingerprint(labels),
		Attributes: labelsJSON(labels), Bucket: bucket, Count: count, Sum: float64(count * 10), Min: 1, Max: float64(count), Last: 5, ObsCount: count, Extra: extra,
	}
}

// Rollup rows take the merge environment as a label, which changes the series
// fingerprint, and a row that meets the target's series in one bucket adds to it.
func TestMergeProjectRollupLabelsAndCollisions(t *testing.T) {
	f := newMergeFixture(t)
	repo := f.repo
	b := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	plain := map[string]string{"host": "a"}
	staged := map[string]string{"host": "a", "deployment.environment": "staging"}
	own := map[string]string{"host": "b", "deployment.environment": "qa"}
	if err := repo.UpsertMetricRollups([]MetricRollup{
		rollupRow(f.source.ID, plain, b, 3, `{"h":1}`),
		rollupRow(f.source.ID, own, b, 2, ""),
		rollupRow(f.source.ID, plain, b.Add(5*time.Minute), 4, ""),
		rollupRow(f.target.ID, staged, b, 5, `{"h":2}`),
	}); err != nil {
		t.Fatal(err)
	}
	coarse := rollupRow(f.source.ID, plain, b.Truncate(time.Hour), 7, "")
	coarse.StepSeconds = 3600
	if err := repo.UpsertCoarseRollups(context.Background(), []MetricRollup{coarse}); err != nil {
		t.Fatal(err)
	}

	results, err := repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	moved, collided := map[string]int64{}, map[string]int64{}
	for _, r := range results {
		moved[r.Table] += r.Moved
		collided[r.Table] += r.Collided
	}
	if moved["metric_rollups"] != 3 || collided["metric_rollups"] != 1 || moved["metric_rollups_coarse"] != 1 || collided["metric_rollups_coarse"] != 0 {
		t.Errorf("moved %v collided %v", moved, collided)
	}
	if n := mergeCount(t, repo, `SELECT count(*) FROM metric_rollups WHERE project_id = ?`, f.source.ID); n != 0 {
		t.Errorf("source keeps %d rollups", n)
	}

	var count, obs int64
	var extra string
	if err := repo.db.QueryRow(`SELECT count, obs_count, COALESCE(extra, '') FROM metric_rollups WHERE project_id = ? AND attr_fingerprint = ? AND bucket = ?`,
		f.target.ID, metrics.Fingerprint(staged), b).Scan(&count, &obs, &extra); err != nil {
		t.Fatal(err)
	}
	if count != 8 || obs != 8 || extra != `{"h":2}` {
		t.Errorf("folded rollup = count %d obs %d extra %q, want 8, 8 and the target's snapshot", count, obs, extra)
	}
	// The next bucket has no counterpart: the row moved and carries the label.
	var attrs, fp string
	if err := repo.db.QueryRow(`SELECT attributes, attr_fingerprint FROM metric_rollups WHERE project_id = ? AND bucket = ?`,
		f.target.ID, b.Add(5*time.Minute)).Scan(&attrs, &fp); err != nil {
		t.Fatal(err)
	}
	if fp != metrics.Fingerprint(staged) || metrics.ParseAttributes([]byte(attrs))["deployment.environment"] != "staging" {
		t.Errorf("moved rollup = %s (%s), want the staging label and its fingerprint", attrs, fp)
	}
	// A series with an environment of its own keeps it.
	if n := mergeCount(t, repo, `SELECT count(*) FROM metric_rollups WHERE project_id = ? AND attr_fingerprint = ?`, f.target.ID, metrics.Fingerprint(own)); n != 1 {
		t.Errorf("qa series rows = %d, want 1", n)
	}
	if n := mergeCount(t, repo, `SELECT count(*) FROM metric_rollups_coarse WHERE project_id = ? AND attr_fingerprint = ? AND step_seconds = 3600`, f.target.ID, metrics.Fingerprint(staged)); n != 1 {
		t.Errorf("coarse rows in the target's staging series = %d, want 1", n)
	}
}

func TestMergeProjectMovesLogsMetricsPromptsOnOneFile(t *testing.T) {
	f := newMergeFixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		rec := makeLogRecord(f.source.ID, "", "line", 9)
		if i == 0 {
			rec.Attributes = json.RawMessage(`{"deployment.environment":"qa"}`)
		}
		if err := f.repo.InsertLogs(ctx, []model.LogRecord{rec}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repo.InsertMetrics(ctx, []model.MetricRecord{makeMetricRecord(f.source.ID, "m", model.MetricTypeGauge, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.InsertPromptRecords([]PromptRecord{{ProjectID: f.source.ID, TraceID: "t", SpanID: "s", Service: "web", Name: "n", StartTimeUs: 1}}); err != nil {
		t.Fatal(err)
	}
	mergeExec(t, f.repo, `INSERT INTO kept_logs (id, project_id, time_unix_nano, attributes) VALUES (1, ?, 1, '{}')`, f.source.ID)

	if _, err := f.repo.MergeProject(ctx, f.spec(), MergeOptions{Batch: 2}); err != nil {
		t.Fatal(err)
	}
	count := func(table string, fam engine.Family, where string, args ...any) int64 {
		var n int64
		if err := f.repo.reader(fam).QueryRow(`SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for table, fam := range map[string]engine.Family{"logs": engine.FamilyLogs, "metrics": engine.FamilyMetrics, "prompt_records": engine.FamilyPrompts, "kept_logs": engine.FamilyCore} {
		if n := count(table, fam, "project_id = ?", f.source.ID); n != 0 {
			t.Errorf("%s keeps %d source rows", table, n)
		}
		if n := count(table, fam, "project_id = ?", f.target.ID); n == 0 {
			t.Errorf("%s moved nothing", table)
		}
	}
	if n := count("logs", engine.FamilyLogs, `json_extract(attributes, '$."deployment.environment"') = 'staging'`); n != 2 {
		t.Errorf("logs stamped staging = %d, want 2", n)
	}
	if n := count("logs", engine.FamilyLogs, `json_extract(attributes, '$."deployment.environment"') = 'qa'`); n != 1 {
		t.Errorf("logs keeping qa = %d, want 1", n)
	}
	if n := count("kept_logs", engine.FamilyCore, `json_extract(attributes, '$."deployment.environment"') = 'staging'`); n != 1 {
		t.Errorf("kept logs stamped staging = %d, want 1", n)
	}
}

// With sharding on, every active shard is a step of its own, progress keeps
// the shard, and a second run finds nothing left.
func TestMergeProjectMovesEveryShard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	clock := mustTime(t, "2026-10-04T10:00:00Z")
	store := openShardedStorage(t, path, &clock)
	repo := ForStorage(store)
	src, err := repo.CreateProject("main-staging", "main-staging")
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := repo.CreateProject("main", "main")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for day := 0; day < 2; day++ {
		for i := 0; i < 3; i++ {
			if err := repo.InsertLogs(ctx, []model.LogRecord{makeLogRecord(src.ID, "", "line", 9), makeLogRecord(tgt.ID, "", "own", 9)}); err != nil {
				t.Fatal(err)
			}
		}
		clock = clock.Add(24 * time.Hour)
	}
	spec := MergeSpec{SourceID: src.ID, TargetID: tgt.ID, SourceSlug: src.Slug, Environment: "staging"}
	results, err := repo.MergeProject(ctx, spec, MergeOptions{Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	shards := map[string]int64{}
	for _, r := range results {
		if r.Table == "logs" {
			shards[r.Shard] += r.Moved
		}
	}
	if len(shards) != 2 || shards["2026-10-04"] != 3 || shards["2026-10-05"] != 3 {
		t.Errorf("logs moved per shard = %v, want 3 in each of two shards", shards)
	}
	for _, file := range []string{"logs-20261004.db", "logs-20261005.db"} {
		db, err := sql.Open("sqlite", filepath.Join(engine.ShardsDir(path), file)+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var left, mine int
		if err := db.QueryRow(`SELECT count(*) FILTER (WHERE project_id = ?), count(*) FILTER (WHERE project_id = ?) FROM logs`, src.ID, tgt.ID).Scan(&left, &mine); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if left != 0 || mine != 6 {
			t.Errorf("%s: source %d target %d, want 0 and 6", file, left, mine)
		}
	}
	progress, err := repo.MergeProgress(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int64{}
	for _, p := range progress {
		if p.Table == "logs" {
			seen[p.Shard] = p.Moved
		}
	}
	if len(seen) != 2 || seen["2026-10-04"] != 3 {
		t.Errorf("stored progress = %v, want a row per shard", seen)
	}
	again, err := repo.MergeProject(ctx, spec, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Moved != 0 {
			t.Errorf("second run moved %d rows of %s", r.Moved, r.Table)
		}
	}
}

// Every table with a project_id column is either a step of the merge or named
// here with the reason it is not, so a table added later cannot be left behind
// in an archived project.
func TestEveryProjectTableIsMerged(t *testing.T) {
	repo := setupCombinedTestDB(t)
	plan, err := repo.mergePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	merged := map[string]bool{}
	for _, s := range plan {
		merged[s.table] = true
	}
	exempt := map[string]string{}
	rows, err := repo.db.Query(`SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'project_id' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !merged[name] && exempt[name] == "" {
			t.Errorf("table %s has a project_id column and no merge step", name)
		}
	}
}

func TestMergeArchivesTheSource(t *testing.T) {
	f := spansMergeFixture(t)
	if got, err := f.repo.ArchivedInto(f.source.ID); err != nil || got != 0 {
		t.Fatalf("before the merge ArchivedInto = %d, %v", got, err)
	}
	stop := errors.New("stop")
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 1, OnBatch: func(MergeStepResult) error { return stop }}); !errors.Is(err, stop) {
		t.Fatalf("stopped run = %v", err)
	}
	if got, _ := f.repo.ArchivedInto(f.source.ID); got != 0 {
		t.Errorf("a stopped merge resolves to %d, want the source to stay live", got)
	}
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, err := f.repo.ArchivedInto(f.source.ID); err != nil || got != f.target.ID {
		t.Errorf("after the merge ArchivedInto = %d, %v, want %d", got, err, f.target.ID)
	}
	list, err := f.repo.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == f.source.ID {
			t.Error("the archived source is still in the project list")
		}
	}
	if p, err := f.repo.GetProjectBySlug(f.source.Slug); err != nil || p.ID != f.source.ID {
		t.Errorf("the archived slug no longer resolves: %+v, %v", p, err)
	}
}
