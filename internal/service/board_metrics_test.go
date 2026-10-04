package service

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

func metricPanel(name string, groupBy ...string) PanelRequest {
	return PanelRequest{
		Title:      name,
		View:       PanelViewMetric,
		Definition: QueryDefinition{Metric: &MetricPanel{Name: name, GroupBy: groupBy}},
	}
}

// TestMetricPanelStoresMetricQuery: a metric panel needs no span calculation,
// and the board returns the metric name and group-by it was saved with.
func TestMetricPanelStoresMetricQuery(t *testing.T) {
	svc, project := boardService(t)
	board, err := svc.CreateBoard(project, "Storage", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddPanel(board, metricPanel("spanbarn.retention.deleted", "table")); err != nil {
		t.Fatalf("AddPanel: %v", err)
	}
	b, err := svc.GetBoard(board)
	if err != nil {
		t.Fatal(err)
	}
	var def QueryDefinition
	if err := json.Unmarshal(b.Panels[0].Query.Definition, &def); err != nil {
		t.Fatal(err)
	}
	if b.Panels[0].View != PanelViewMetric || def.Metric == nil ||
		def.Metric.Name != "spanbarn.retention.deleted" || len(def.Metric.GroupBy) != 1 || def.Metric.GroupBy[0] != "table" {
		t.Fatalf("panel = view %q, definition %s", b.Panels[0].View, b.Panels[0].Query.Definition)
	}
}

func TestMetricPanelValidation(t *testing.T) {
	svc, project := boardService(t)
	board, err := svc.CreateBoard(project, "Storage", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	spanWithMetric := validPanel()
	spanWithMetric.Definition.Metric = &MetricPanel{Name: "x"}
	cases := map[string]PanelRequest{
		"no metric":           {Title: "t", View: PanelViewMetric},
		"empty name":          metricPanel(""),
		"bad group key":       metricPanel("spanbarn.db.bytes", "bad key;"),
		"metric on span view": spanWithMetric,
	}
	for name, req := range cases {
		if _, err := svc.AddPanel(board, req); !errors.Is(err, filter.ErrInvalid) {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
}

// TestMetricPanelKeepsItsView: a metric panel cannot turn into a span table,
// and a span panel cannot turn into a metric panel: their queries differ.
func TestMetricPanelKeepsItsView(t *testing.T) {
	svc, project := boardService(t)
	board, err := svc.CreateBoard(project, "Mixed", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	metricID, err := svc.AddPanel(board, metricPanel("spanbarn.db.bytes"))
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := svc.AddPanel(board, validPanel())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdatePanel(board, metricID, "DB", "table"); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("metric -> table: err = %v, want invalid", err)
	}
	if err := svc.UpdatePanel(board, spanID, "Spans", PanelViewMetric); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("chart -> metric: err = %v, want invalid", err)
	}
	if err := svc.UpdatePanel(board, metricID, "DB size", PanelViewMetric); err != nil {
		t.Errorf("rename metric panel: %v", err)
	}
}

// TestEnsureBoardCreatesOnce: the first call builds the board with its panels
// in order; later calls find it by name and leave it alone, edits included.
func TestEnsureBoardCreatesOnce(t *testing.T) {
	svc, project := boardService(t)
	spec := BoardSpec{
		Name:      "Storage",
		TimeRange: "7d",
		Refresh:   300,
		Panels:    []PanelRequest{metricPanel("spanbarn.disk.used_pct"), metricPanel("spanbarn.db.bytes")},
	}
	id, created, err := svc.EnsureBoard(project, spec)
	if err != nil || !created {
		t.Fatalf("first EnsureBoard = %d, %v, %v", id, created, err)
	}
	b, err := svc.GetBoard(id)
	if err != nil {
		t.Fatal(err)
	}
	if b.TimeRange != "7d" || b.RefreshSeconds != 300 || len(b.Panels) != 2 || b.Panels[0].Title != "spanbarn.disk.used_pct" {
		t.Fatalf("board = %+v", b)
	}
	if err := svc.DeletePanel(id, b.Panels[1].ID); err != nil {
		t.Fatal(err)
	}

	again, created, err := svc.EnsureBoard(project, spec)
	if err != nil || created || again != id {
		t.Fatalf("second EnsureBoard = %d, %v, %v; want %d, false", again, created, err, id)
	}
	if b, _ := svc.GetBoard(id); len(b.Panels) != 1 {
		t.Errorf("second EnsureBoard touched the board: %d panels, want the 1 left after the edit", len(b.Panels))
	}
}

// TestEnsureBoardRejectsBadSpecBeforeCreating: an invalid panel must not leave
// an empty board behind.
func TestEnsureBoardRejectsBadSpecBeforeCreating(t *testing.T) {
	svc, project := boardService(t)
	_, _, err := svc.EnsureBoard(project, BoardSpec{Name: "Broken", Panels: []PanelRequest{metricPanel("")}})
	if !errors.Is(err, filter.ErrInvalid) {
		t.Fatalf("err = %v, want invalid", err)
	}
	if boards, _ := svc.ListBoards(project); len(boards) != 0 {
		t.Errorf("%d boards left behind", len(boards))
	}
}
