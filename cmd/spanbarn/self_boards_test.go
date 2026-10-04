package main

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

// TestEnsureSelfBoardsOnSelfProject: the boards land on the project of the
// self-metrics key, every panel is a metric panel, and a second start adds
// nothing.
func TestEnsureSelfBoardsOnSelfProject(t *testing.T) {
	repo, db := testRepo(t)
	defer db.Close()
	if _, err := repo.CreateProject("other", "Other"); err != nil {
		t.Fatal(err)
	}
	self, err := repo.CreateProject("spanbarn", "SpanBarn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateAPIKey(self.ID, "self", auth.HashKey("self-key"), "ingest"); err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Self.APIKey = "self-key"

	ensureSelfBoards(cfg, repo, slog.Default())
	ensureSelfBoards(cfg, repo, slog.Default())

	boards, err := repo.ListBoards(self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != len(selfBoards()) {
		t.Fatalf("%d boards on the self project, want %d", len(boards), len(selfBoards()))
	}
	for _, b := range boards {
		full, err := repo.GetBoard(b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(full.Panels) == 0 {
			t.Errorf("board %q has no panels", b.Name)
		}
		for _, p := range full.Panels {
			var def service.QueryDefinition
			if err := json.Unmarshal(p.Query.Definition, &def); err != nil || p.View != service.PanelViewMetric || def.Metric == nil {
				t.Errorf("board %q panel %q: view %q, definition %s", b.Name, p.Title, p.View, p.Query.Definition)
			}
		}
	}
}

// TestEnsureSelfBoardsSkipsWithoutProjectKey: a key that is not in the
// database (the static admin key, say) belongs to no project, so no board is
// made anywhere.
func TestEnsureSelfBoardsSkipsWithoutProjectKey(t *testing.T) {
	repo, db := testRepo(t)
	defer db.Close()
	p, err := repo.CreateProject("spanbarn", "SpanBarn")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.APIKey = "static-admin-key"

	ensureSelfBoards(cfg, repo, slog.Default())

	if boards, _ := repo.ListBoards(p.ID); len(boards) != 0 {
		t.Errorf("%d boards created without a project key", len(boards))
	}
}
