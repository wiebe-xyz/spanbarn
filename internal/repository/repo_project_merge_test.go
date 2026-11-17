package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func mergeExec(t *testing.T, repo *Repository, q string, args ...any) {
	t.Helper()
	if err := repo.execHigh(engine.FamilyCore, func(db *sql.DB) error {
		_, err := db.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func mergeCount(t *testing.T, repo *Repository, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := repo.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

type mergeFixture struct {
	repo           *Repository
	source, target Project
}

// newMergeFixture holds a source and a target project. The source has five keys,
// alerts, boards, releases and a spread of settings, and the target holds the
// rows that clash with them.
func newMergeFixture(t *testing.T) mergeFixture {
	t.Helper()
	repo := setupTestDB(t)
	src, err := repo.CreateProject("main-staging", "main-staging")
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := repo.CreateProject("main", "main")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		mergeExec(t, repo, `INSERT INTO api_keys (project_id, name, key_hash, environment) VALUES (?, ?, ?, ?)`,
			src.ID, "key", "hash"+string(rune('a'+i)), map[bool]string{true: "qa", false: ""}[i == 0])
		mergeExec(t, repo, `INSERT INTO releases (project_id, version, released_at) VALUES (?, ?, datetime('now'))`, src.ID, "v1."+string(rune('a'+i)))
		mergeExec(t, repo, `INSERT INTO alerts (project_id, service, type, threshold) VALUES (?, 'web', 'error_rate', 1)`, src.ID)
	}
	mergeExec(t, repo, `INSERT INTO boards (project_id, name) VALUES (?, 'Overview')`, src.ID)
	mergeExec(t, repo, `INSERT INTO slos (project_id, name, target, window_days) VALUES (?, 'availability', 0.99, 30), (?, 'availability', 0.95, 7), (?, 'latency', 0.9, 7)`, src.ID, tgt.ID, src.ID)
	mergeExec(t, repo, `INSERT INTO calculated_fields (project_id, name, expression) VALUES (?, 'rate', 'a/b'), (?, 'rate', 'c/d')`, src.ID, tgt.ID)
	mergeExec(t, repo, `INSERT INTO trace_exclusions (project_id, operation) VALUES (?, 'GET /health'), (?, 'GET /health'), (?, 'GET /ready')`, src.ID, tgt.ID, src.ID)
	mergeExec(t, repo, `INSERT INTO pinned_traces (project_id, trace_id) VALUES (?, 't1'), (?, 't1'), (?, 't2')`, src.ID, tgt.ID, src.ID)
	for b := int64(100); b < 106; b++ {
		mergeExec(t, repo, `INSERT INTO span_ledger (bucket, project_id, environment, accepted, processed) VALUES (?, ?, '', 10, 8)`, b, src.ID)
	}
	mergeExec(t, repo, `INSERT INTO span_ledger (bucket, project_id, environment, accepted, processed) VALUES (100, ?, 'staging', 5, 5)`, tgt.ID)
	return mergeFixture{repo: repo, source: src, target: tgt}
}

func (f mergeFixture) spec() MergeSpec {
	return MergeSpec{SourceID: f.source.ID, TargetID: f.target.ID, SourceSlug: f.source.Slug, Environment: "staging"}
}

func TestMergeProjectMovesCoreTablesAndRepointsKeys(t *testing.T) {
	f := newMergeFixture(t)
	results, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2})
	if err != nil {
		t.Fatalf("MergeProject: %v", err)
	}
	moved := map[string]int64{}
	for _, r := range results {
		moved[r.Table] = r.Moved
	}
	want := map[string]int64{"api_keys": 5, "alerts": 5, "boards": 1, "releases": 5, "slos": 2, "calculated_fields": 1, "trace_exclusions": 1, "pinned_traces": 1, "span_ledger": 6}
	for table, n := range want {
		if moved[table] != n {
			t.Errorf("%s moved %d rows, want %d", table, moved[table], n)
		}
	}
	for _, table := range []string{"api_keys", "alerts", "boards", "releases", "slos", "calculated_fields", "trace_exclusions", "pinned_traces", "span_ledger"} {
		if n := mergeCount(t, f.repo, "SELECT count(*) FROM "+table+" WHERE project_id = ?", f.source.ID); n != 0 {
			t.Errorf("%s keeps %d rows of the source", table, n)
		}
	}
	// A key with its own environment keeps it; the rest take the merge's.
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM api_keys WHERE project_id = ? AND environment = 'qa'`, f.target.ID); n != 1 {
		t.Errorf("keys with their own environment = %d, want 1", n)
	}
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM api_keys WHERE project_id = ? AND environment = 'staging'`, f.target.ID); n != 4 {
		t.Errorf("keys stamped staging = %d, want 4", n)
	}
	if got, err := f.repo.MergedInto(f.source.ID); err != nil || got != f.target.ID {
		t.Errorf("MergedInto = %d, %v, want %d", got, err, f.target.ID)
	}
}

func TestMergeProjectSettingsCollisions(t *testing.T) {
	f := newMergeFixture(t)
	results, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2})
	if err != nil {
		t.Fatalf("MergeProject: %v", err)
	}
	// Both SLOs named availability survive, the source one with its slug.
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM slos WHERE project_id = ? AND name = 'availability (main-staging)' AND target = 0.99`, f.target.ID); n != 1 {
		t.Errorf("renamed SLO rows = %d, want 1", n)
	}
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM calculated_fields WHERE project_id = ? AND name IN ('rate', 'rate (main-staging)')`, f.target.ID); n != 2 {
		t.Errorf("calculated fields = %d, want both", n)
	}
	// A duplicate exclusion and a duplicate pin stay once, under the target.
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM trace_exclusions WHERE project_id = ?`, f.target.ID); n != 2 {
		t.Errorf("trace exclusions = %d, want 2", n)
	}
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM pinned_traces WHERE project_id = ?`, f.target.ID); n != 2 {
		t.Errorf("pinned traces = %d, want 2", n)
	}
	collided := map[string]int64{}
	for _, r := range results {
		collided[r.Table] = r.Collided
	}
	for table, n := range map[string]int64{"slos": 1, "calculated_fields": 1, "trace_exclusions": 1, "pinned_traces": 1, "span_ledger": 1} {
		if collided[table] != n {
			t.Errorf("%s collided %d, want %d", table, collided[table], n)
		}
	}
}

// The ledger rows of one bucket and environment add up when both projects
// tallied them, and the totals of the two projects are the same before and after.
func TestMergeProjectLedgerSumsCollisions(t *testing.T) {
	f := newMergeFixture(t)
	before := mergeCount(t, f.repo, `SELECT sum(accepted) FROM span_ledger`)
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2}); err != nil {
		t.Fatalf("MergeProject: %v", err)
	}
	if after := mergeCount(t, f.repo, `SELECT sum(accepted) FROM span_ledger`); after != before {
		t.Errorf("accepted total %d after, %d before", after, before)
	}
	if n := mergeCount(t, f.repo, `SELECT accepted FROM span_ledger WHERE project_id = ? AND bucket = 100 AND environment = 'staging'`, f.target.ID); n != 15 {
		t.Errorf("colliding bucket accepted = %d, want 15", n)
	}
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM span_ledger WHERE project_id = ? AND environment = 'staging'`, f.target.ID); n != 6 {
		t.Errorf("target staging buckets = %d, want 6", n)
	}
}

// A merge that stops after two batches resumes on the second call, and the
// progress counts equal the rows moved with nothing counted twice.
func TestMergeProjectResumesAfterAStop(t *testing.T) {
	f := newMergeFixture(t)
	stop := errors.New("stopped")
	batches := 0
	_, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2, OnBatch: func(MergeStepResult) error {
		batches++
		if batches == 2 {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatalf("first run = %v, want the stop", err)
	}
	if n := mergeCount(t, f.repo, `SELECT count(*) FROM api_keys WHERE project_id = ?`, f.source.ID); n != 1 {
		t.Fatalf("source keys after two batches = %d, want 1 left", n)
	}
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	progress, err := f.repo.MergeProgress(f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range progress {
		if p.Table == "api_keys" && p.Moved != 5 {
			t.Errorf("api_keys progress = %d moved, want 5", p.Moved)
		}
		if p.Table == "releases" && p.Moved != 5 {
			t.Errorf("releases progress = %d moved, want 5", p.Moved)
		}
	}
	// A third run finds nothing to move and changes no count.
	again, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{Batch: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		if r.Moved != 0 || r.Collided != 0 {
			t.Errorf("third run moved %+v", r)
		}
	}
}

func TestMergeProjectRefusesConflictingMerges(t *testing.T) {
	f := newMergeFixture(t)
	other, err := f.repo.CreateProject("other", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.MergeProject(context.Background(), MergeSpec{SourceID: f.source.ID, TargetID: f.source.ID}, MergeOptions{}); !errors.Is(err, ErrMergeConflict) {
		t.Errorf("same project = %v, want ErrMergeConflict", err)
	}
	if _, err := f.repo.MergeProject(context.Background(), f.spec(), MergeOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := f.spec()
	spec.TargetID = other.ID
	if _, err := f.repo.MergeProject(context.Background(), spec, MergeOptions{}); !errors.Is(err, ErrMergeConflict) {
		t.Errorf("second target = %v, want ErrMergeConflict", err)
	}
	spec = f.spec()
	spec.Environment = "testing"
	if _, err := f.repo.MergeProject(context.Background(), spec, MergeOptions{}); !errors.Is(err, ErrMergeConflict) {
		t.Errorf("second environment = %v, want ErrMergeConflict", err)
	}
	if got, _ := f.repo.MergedInto(other.ID); got != 0 {
		t.Errorf("unmerged project resolves to %d, want 0", got)
	}
}
