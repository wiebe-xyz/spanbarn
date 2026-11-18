package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
)

func archiveFixture(t *testing.T, setup func(repo *repository.Repository, empty, withKey, withLogs repository.Project)) config.Config {
	t.Helper()
	cfg := config.Config{DBPath: filepath.Join(t.TempDir(), "spanbarn.db"), Shards: true}
	cfg.Retention.LogHours = 48
	cfg.Retention.MetricsDays = 7
	cfg.Retention.PromptDays = 14
	store, err := engine.OpenStorage(context.Background(), cfg.DBPath, engine.StorageOptions{Shards: shardRetention(cfg)})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := repository.ForStorage(store)
	mk := func(slug string) repository.Project {
		p, err := repo.CreateProject(slug, slug)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	empty, withKey, withLogs := mk("empty"), mk("keyed"), mk("logged")
	if _, err := repo.CreateAPIKey(withKey.ID, "web", "hash-web", "ingest"); err != nil {
		t.Fatal(err)
	}
	rec := model.LogRecord{ProjectID: withLogs.ID, Body: "line", SeverityNumber: 9, SeverityText: "INFO",
		TimeUnixNano: uint64(time.Now().UnixNano()), Attributes: []byte(`{}`)}
	if err := repo.InsertLogs(context.Background(), []model.LogRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(repo, empty, withKey, withLogs)
	}
	return cfg
}

func listedSlugs(t *testing.T, cfg config.Config) map[string]bool {
	t.Helper()
	repo, db, err := openMergeDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ps, err := repo.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range ps {
		out[p.Slug] = true
	}
	return out
}

// A project with no key and no row, in any file or shard, is archived and
// leaves the project list.
func TestProjectArchiveEmptyProject(t *testing.T) {
	cfg := archiveFixture(t, nil)
	if err := runProjectCmd(cfg, []string{"archive", "--slug=empty"}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := listedSlugs(t, cfg); got["empty"] || !got["keyed"] || !got["logged"] {
		t.Errorf("listed projects after archive = %v", got)
	}
}

// A project with a key, or with a row in a logs shard, refuses and stays listed.
func TestProjectArchiveRefusesProjectWithData(t *testing.T) {
	cfg := archiveFixture(t, nil)
	for _, slug := range []string{"keyed", "logged"} {
		err := runProjectCmd(cfg, []string{"archive", "--slug=" + slug})
		if !errors.Is(err, repository.ErrProjectNotEmpty) {
			t.Errorf("archive %s = %v, want ErrProjectNotEmpty", slug, err)
		}
	}
	if got := listedSlugs(t, cfg); !got["keyed"] || !got["logged"] {
		t.Errorf("listed projects after refused archives = %v", got)
	}
}
