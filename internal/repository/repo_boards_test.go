package repository

import (
	"errors"
	"testing"
	"time"
)

func boardFixture(t *testing.T) (*Repository, int64, int64) {
	t.Helper()
	repo := setupTestDB(t)
	project, err := repo.CreateProject("svc", "Boards Test")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	id, err := repo.CreateBoard(project.ID, "Overview", "24h", 60)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	return repo, project.ID, id
}

func addPanel(t *testing.T, repo *Repository, board int64, title string) int64 {
	t.Helper()
	id, err := repo.AddPanelWithQuery(board, SavedQuery{
		Name:       title,
		Filters:    []byte(`{"match":"and","filters":[{"key":"kind","op":"=","value":"server"}]}`),
		Definition: []byte(`{"groupBy":["url.path"],"calcs":["count"]}`),
	}, title, "table")
	if err != nil {
		t.Fatalf("AddPanelWithQuery: %v", err)
	}
	return id
}

func TestBoardPanelsKeepOrderAndQuery(t *testing.T) {
	repo, projectID, board := boardFixture(t)
	a := addPanel(t, repo, board, "a")
	b := addPanel(t, repo, board, "b")
	c := addPanel(t, repo, board, "c")

	got, err := repo.GetBoard(board)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Overview" || got.TimeRange != "24h" || got.RefreshSeconds != 60 || len(got.Panels) != 3 {
		t.Fatalf("board = %+v", got)
	}
	p := got.Panels[0]
	if p.ID != a || p.Title != "a" || p.View != "table" || p.Query.ProjectID != projectID ||
		string(p.Query.Definition) != `{"groupBy":["url.path"],"calcs":["count"]}` || len(p.Query.Filters) == 0 {
		t.Fatalf("first panel = %+v", p)
	}

	if err := repo.ReorderPanels(board, []int64{c, a, b}); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetBoard(board)
	if got.Panels[0].ID != c || got.Panels[1].ID != a || got.Panels[2].ID != b {
		t.Fatalf("order after reorder = %v %v %v", got.Panels[0].ID, got.Panels[1].ID, got.Panels[2].ID)
	}
	if err := repo.ReorderPanels(board, []int64{a, b}); !errors.Is(err, ErrPanelSet) {
		t.Fatalf("partial order err = %v, want ErrPanelSet", err)
	}
	if err := repo.ReorderPanels(board, []int64{a, b, 999}); !errors.Is(err, ErrPanelSet) {
		t.Fatalf("foreign panel err = %v, want ErrPanelSet", err)
	}
}

func TestBoardUpdateAndPanelEdits(t *testing.T) {
	repo, projectID, board := boardFixture(t)
	panel := addPanel(t, repo, board, "a")

	if err := repo.UpdateBoard(board, "Renamed", "7d", 0); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetBoard(board)
	if got.Name != "Renamed" || got.TimeRange != "7d" || got.RefreshSeconds != 0 {
		t.Fatalf("board = %+v", got)
	}
	if err := repo.UpdateBoard(999, "x", "24h", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown board err = %v", err)
	}

	if err := repo.UpdatePanel(board, panel, "Errors", "chart"); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetBoard(board)
	if got.Panels[0].Title != "Errors" || got.Panels[0].View != "chart" {
		t.Fatalf("panel = %+v", got.Panels[0])
	}
	if err := repo.UpdatePanel(board+1, panel, "x", "table"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("panel of another board err = %v", err)
	}

	list, err := repo.ListBoards(projectID)
	if err != nil || len(list) != 1 || list[0].ID != board {
		t.Fatalf("ListBoards = %+v, %v", list, err)
	}
	other, _ := repo.ListBoards(projectID + 1)
	if len(other) != 0 {
		t.Fatalf("other project sees %d boards", len(other))
	}
}

func queryCount(t *testing.T, repo *Repository, where string) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM saved_queries ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeletingPanelsAndBoardsRemovesTheirQueries(t *testing.T) {
	repo, projectID, board := boardFixture(t)
	// A plain trace filter must survive every board delete.
	if _, err := repo.CreateSavedQuery(SavedQuery{ProjectID: projectID, Name: "plain", Filters: []byte(`{"filters":[]}`)}); err != nil {
		t.Fatal(err)
	}
	a := addPanel(t, repo, board, "a")
	addPanel(t, repo, board, "b")
	if n := queryCount(t, repo, ""); n != 3 {
		t.Fatalf("queries = %d, want 3", n)
	}

	if err := repo.DeletePanel(board, a); err != nil {
		t.Fatal(err)
	}
	if n := queryCount(t, repo, "WHERE definition != ''"); n != 1 {
		t.Fatalf("board queries after panel delete = %d, want 1", n)
	}
	if err := repo.DeletePanel(board, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}

	if err := repo.DeleteBoard(board); err != nil {
		t.Fatal(err)
	}
	if n := queryCount(t, repo, ""); n != 1 {
		t.Fatalf("queries after board delete = %d, want the plain one", n)
	}
	if _, err := repo.GetBoard(board); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBoard after delete err = %v", err)
	}
	if err := repo.DeleteBoard(board); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteBoard err = %v", err)
	}
}

func TestDeleteSavedQueryTakesItsPanels(t *testing.T) {
	repo, projectID, board := boardFixture(t)
	addPanel(t, repo, board, "a")
	list, err := repo.ListSavedQueries(projectID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSavedQueries = %v, %v", list, err)
	}
	if string(list[0].Definition) == "" {
		t.Fatal("definition not returned")
	}
	if err := repo.DeleteSavedQuery(list[0].ID); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.GetBoard(board)
	if len(got.Panels) != 0 {
		t.Fatalf("panels = %d after the query was deleted", len(got.Panels))
	}
}

func TestAddPanelToUnknownBoard(t *testing.T) {
	repo := setupTestDB(t)
	if _, err := repo.AddPanelWithQuery(42, SavedQuery{Name: "x"}, "x", "table"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := queryCount(t, repo, ""); n != 0 {
		t.Fatalf("a failed add left %d queries", n)
	}
}

func TestReleasesInRange(t *testing.T) {
	repo, projectID, _ := boardFixture(t)
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i, v := range []string{"v1", "v2", "v3"} {
		if _, err := repo.CreateRelease(projectID, v, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := repo.CreateProject("svc2", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRelease(second.ID, "other", base); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListReleases(projectID, base.Add(30*time.Minute), base.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Version != "v2" || got[1].Version != "v3" || !got[0].ReleasedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("releases = %+v", got)
	}
}
