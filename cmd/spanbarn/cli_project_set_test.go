package main

import (
	"path/filepath"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/config"
)

func newProjectSetFixture(t *testing.T) (config.Config, int64) {
	t.Helper()
	cfg := config.Config{DBPath: filepath.Join(t.TempDir(), "spanbarn.db")}
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	p, err := repo.CreateProject("claude-code", "claude-code")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	db.Close()
	return cfg, p.ID
}

func settingOf(t *testing.T, cfg config.Config, key string) string {
	t.Helper()
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()
	v, err := repo.GetSetting(key)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	return v
}

// Keeping every trace needs both stages: the trace buffer ratio and the boring
// span ratio. With only the first, kept traces still lose their spans.
func TestProjectSetTraceRatioWritesBothStages(t *testing.T) {
	cfg, id := newProjectSetFixture(t)
	if err := runProjectCmd(cfg, []string{"set", "claude-code", "--trace-ratio=1"}); err != nil {
		t.Fatalf("project set: %v", err)
	}
	for _, key := range []string{
		"ingest.sample_ratio.project." + itoa(id),
		"boring.sample_ratio.project." + itoa(id),
	} {
		if got := settingOf(t, cfg, key); got != "1" {
			t.Errorf("%s = %q, want 1", key, got)
		}
	}
}

func TestProjectSetTraceRatioZeroClears(t *testing.T) {
	cfg, id := newProjectSetFixture(t)
	if err := runProjectCmd(cfg, []string{"set", "claude-code", "--trace-ratio=1"}); err != nil {
		t.Fatal(err)
	}
	if err := runProjectCmd(cfg, []string{"set", "claude-code", "--trace-ratio=0"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"ingest.sample_ratio.project." + itoa(id),
		"boring.sample_ratio.project." + itoa(id),
	} {
		if got := settingOf(t, cfg, key); got != "" {
			t.Errorf("%s = %q, want cleared", key, got)
		}
	}
}

func TestProjectSetRejectsBadInput(t *testing.T) {
	cfg, _ := newProjectSetFixture(t)
	for _, args := range [][]string{
		{"set"},
		{"set", "nope", "--trace-ratio=1"},
		{"set", "claude-code"},
		{"set", "claude-code", "--trace-ratio=-1"},
	} {
		if err := runProjectCmd(cfg, args); err == nil {
			t.Errorf("args %v: want error", args)
		}
	}
}
