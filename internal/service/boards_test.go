package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func boardService(t *testing.T) (*BoardService, int64) {
	t.Helper()
	db, err := repository.NewDB(":memory:")
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := repository.Migrate(db.DB); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	repo := repository.NewRepository(db.DB)
	p, err := repo.CreateProject("svc", "Boards")
	if err != nil {
		t.Fatal(err)
	}
	return NewBoardService(repo, nil), p.ID
}

func validPanel() PanelRequest {
	expr, err := filter.Parse(`{"filters":[{"key":"status","op":"=","value":"error"}]}`)
	if err != nil {
		panic(err)
	}
	return PanelRequest{
		Title:      "Errors by path",
		View:       "chart",
		Filters:    expr,
		Definition: QueryDefinition{GroupBy: []string{"url.path"}, Calcs: []string{"count", "p95"}, ChartCalc: "p95", Limit: 10},
	}
}

func TestBoardSettingsValidation(t *testing.T) {
	svc, project := boardService(t)
	cases := []struct {
		name    string
		project int64
		board   string
		rng     string
		refresh int
	}{
		{"no project", 0, "b", "24h", 0},
		{"empty name", project, "  ", "24h", 0},
		{"long name", project, strings.Repeat("x", 121), "24h", 0},
		{"bad range", project, "b", "90d", 0},
		{"bad refresh", project, "b", "24h", 7},
	}
	for _, c := range cases {
		if _, err := svc.CreateBoard(c.project, c.board, c.rng, c.refresh); !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
	}
	id, err := svc.CreateBoard(project, " Overview ", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetBoard(id)
	if err != nil || b.Name != "Overview" || b.TimeRange != DefaultBoardRange || b.RefreshSeconds != 30 {
		t.Fatalf("board = %+v, %v", b, err)
	}
	if err := svc.UpdateBoard(id, "Overview", "7d", 900); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateBoard(id, "Overview", "7d", 5); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("bad refresh err = %v", err)
	}
	if err := svc.UpdateBoard(999, "x", "7d", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown board err = %v", err)
	}
}

func TestAddPanelValidatesTheDefinition(t *testing.T) {
	svc, project := boardService(t)
	board, _ := svc.CreateBoard(project, "b", "24h", 0)

	bad := map[string]func(*PanelRequest){
		"bad view":            func(r *PanelRequest) { r.View = "pie" },
		"no calcs":            func(r *PanelRequest) { r.Definition.Calcs = nil },
		"unknown calc":        func(r *PanelRequest) { r.Definition.Calcs = []string{"median"} },
		"bad distinct":        func(r *PanelRequest) { r.Definition.Calcs = []string{"count_distinct:"} },
		"five group by":       func(r *PanelRequest) { r.Definition.GroupBy = []string{"a", "b", "c", "d", "e"} },
		"duplicate group by":  func(r *PanelRequest) { r.Definition.GroupBy = []string{"a", "a"} },
		"chart calc missing":  func(r *PanelRequest) { r.Definition.ChartCalc = "p99" },
		"order by missing":    func(r *PanelRequest) { r.Definition.OrderBy = "p99" },
		"limit too large":     func(r *PanelRequest) { r.Definition.Limit = 101 },
		"sample out of range": func(r *PanelRequest) { r.Definition.Sample = 5000 },
		"long title":          func(r *PanelRequest) { r.Title = strings.Repeat("x", 201) },
	}
	for name, mutate := range bad {
		req := validPanel()
		mutate(&req)
		if _, err := svc.AddPanel(board, req); !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := svc.AddPanel(999, validPanel()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown board err = %v", err)
	}

	req := validPanel()
	req.Title, req.View = "", ""
	id, err := svc.AddPanel(board, req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetBoard(board)
	if len(got.Panels) != 1 || got.Panels[0].ID != id || got.Panels[0].Title != "Untitled panel" || got.Panels[0].View != "table" {
		t.Fatalf("panels = %+v", got.Panels)
	}
}

func TestBoardHoldsAtMostFiftyPanels(t *testing.T) {
	svc, project := boardService(t)
	board, _ := svc.CreateBoard(project, "b", "24h", 0)
	for i := 0; i < maxPanels; i++ {
		if _, err := svc.AddPanel(board, validPanel()); err != nil {
			t.Fatalf("panel %d: %v", i, err)
		}
	}
	if _, err := svc.AddPanel(board, validPanel()); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("51st panel err = %v, want ErrInvalid", err)
	}
}

func TestPanelEditReorderAndDelete(t *testing.T) {
	svc, project := boardService(t)
	board, _ := svc.CreateBoard(project, "b", "24h", 0)
	a, _ := svc.AddPanel(board, validPanel())
	b, _ := svc.AddPanel(board, validPanel())

	if err := svc.UpdatePanel(board, a, "Renamed", "table"); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdatePanel(board, a, "", "table"); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("empty title err = %v", err)
	}
	if err := svc.UpdatePanel(board, a, "x", "pie"); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("bad view err = %v", err)
	}
	if err := svc.UpdatePanel(board, 999, "x", "table"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown panel err = %v", err)
	}
	if err := svc.ReorderPanels(board, []int64{b, a}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReorderPanels(board, []int64{b}); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("partial order err = %v, want ErrInvalid", err)
	}
	got, _ := svc.GetBoard(board)
	if got.Panels[0].ID != b || got.Panels[1].Title != "Renamed" {
		t.Fatalf("panels = %+v", got.Panels)
	}
	if err := svc.DeletePanel(board, b); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteBoard(board); err != nil {
		t.Fatal(err)
	}
	boards, err := svc.ListBoards(project)
	if err != nil || boards == nil || len(boards) != 0 {
		t.Fatalf("ListBoards = %v, %v", boards, err)
	}
}

func TestReleaseMarkers(t *testing.T) {
	svc, project := boardService(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	if _, err := svc.CreateRelease(project, "v1.2.3", at); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRelease(project, "now", time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		project int64
		version string
	}{{0, "v"}, {project, " "}, {project, strings.Repeat("v", 121)}} {
		if _, err := svc.CreateRelease(c.project, c.version, at); !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%+v: err = %v, want ErrInvalid", c, err)
		}
	}
	got, err := svc.ListReleases(project, at.Add(-time.Minute), time.Now().Add(time.Minute))
	if err != nil || len(got) != 2 || got[0].Version != "v1.2.3" || got[1].Version != "now" {
		t.Fatalf("releases = %+v, %v", got, err)
	}
	if _, err := svc.ListReleases(0, at, at); !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("no project err = %v", err)
	}
}
