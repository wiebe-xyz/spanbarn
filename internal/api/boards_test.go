package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type boardJSON struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	TimeRange      string `json:"timeRange"`
	RefreshSeconds int    `json:"refreshSeconds"`
	Panels         []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		View  string `json:"view"`
		Query struct {
			Filters    json.RawMessage `json:"filters"`
			Definition struct {
				GroupBy   []string `json:"groupBy"`
				Calcs     []string `json:"calcs"`
				ChartCalc string   `json:"chartCalc"`
			} `json:"definition"`
		} `json:"query"`
	} `json:"panels"`
}

// boardServer is the query test server with a repository attached and project 1.
func boardServer(t *testing.T) (*Server, *SessionService, *repository.Repository) {
	t.Helper()
	_, sm, repo := setupQueryTestServer(t)
	if _, err := repo.CreateProject("p1", "P1"); err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithQuery(ServerConfig{APIKey: "test-key", Version: "test"}, nil,
		service.NewQueryService(repo, nil, nil), sm, nil, WithRepository(repo))
	return srv, sm, repo
}

func createdID(t *testing.T, code int, body []byte) int64 {
	t.Helper()
	var out struct{ ID int64 }
	if code != http.StatusCreated || json.Unmarshal(body, &out) != nil || out.ID == 0 {
		t.Fatalf("create: %d %s", code, body)
	}
	return out.ID
}

func TestBoardsRequireASession(t *testing.T) {
	srv, _, _ := boardServer(t)
	for _, c := range [][2]string{{"GET", "/api/v1/boards?project_id=1"}, {"POST", "/api/v1/boards"}, {"GET", "/api/v1/releases"}} {
		code, _ := filterReq(t, srv, "", c[0], c[1], "")
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c[0], c[1], code)
		}
	}
}

// A board of three panels is saved, read back and each panel's stored query
// runs against live spans with the board's range.
func TestThreePanelBoardRoundTrip(t *testing.T) {
	srv, sm, repo := boardServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, method, path, agent, status string, dur int64) repository.Span {
		s := healthSpan("t-"+id, id, "", method+" "+path)
		s.Kind, s.Status, s.DurationUs = "server", status, dur
		s.Attributes = fmt.Sprintf(`{"url.path":%q,"http.request.method":%q,"user_agent.original":%q}`, path, method, agent)
		return s
	}
	if err := repo.InsertSpans([]repository.Span{
		mk("1", "GET", "/a", "curl", "error", 100), mk("2", "GET", "/a", "curl", "error", 300),
		mk("3", "POST", "/b", "firefox", "ok", 50), mk("4", "GET", "/b", "curl", "error", 10),
	}); err != nil {
		t.Fatal(err)
	}

	code, body := filterReq(t, srv, token, "POST", "/api/v1/boards", `{"projectId":1,"name":"Overview","timeRange":"24h","refreshSeconds":60}`)
	board := createdID(t, code, body)

	panels := []string{
		`{"title":"Errors by path","view":"table","filters":{"filters":[{"key":"status","op":"=","value":"error"}]},"definition":{"groupBy":["url.path"],"calcs":["count"]}}`,
		`{"title":"P95 by method","view":"chart","definition":{"groupBy":["http.request.method"],"calcs":["p95"],"chartCalc":"p95"}}`,
		`{"title":"Count by agent","view":"table","definition":{"groupBy":["user_agent.original"],"calcs":["count"]}}`,
	}
	for _, p := range panels {
		code, body = filterReq(t, srv, token, "POST", fmt.Sprintf("/api/v1/boards/%d/panels", board), p)
		createdID(t, code, body)
	}

	code, body = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/boards/%d", board), "")
	var got boardJSON
	if code != 200 || json.Unmarshal(body, &got) != nil || len(got.Panels) != 3 {
		t.Fatalf("get board: %d %s", code, body)
	}
	if got.Name != "Overview" || got.TimeRange != "24h" || got.RefreshSeconds != 60 ||
		got.Panels[0].Title != "Errors by path" || got.Panels[1].View != "chart" || got.Panels[2].Title != "Count by agent" {
		t.Fatalf("board = %s", body)
	}

	// Each panel's stored definition drives the group-by endpoint.
	want := []struct {
		group string
		count float64
	}{{"/a", 2}, {"GET", 0}, {"curl", 3}}
	for i, p := range got.Panels {
		q := filterRange()
		if len(p.Query.Filters) > 0 {
			q.Set("filter", string(p.Query.Filters))
		}
		q["group_by"] = p.Query.Definition.GroupBy
		q["calc"] = p.Query.Definition.Calcs
		code, body = filterReq(t, srv, token, "GET", "/api/v1/analyze?"+q.Encode(), "")
		var res struct {
			Rows []struct {
				Group  []string
				Values []float64
			}
		}
		if code != 200 || json.Unmarshal(body, &res) != nil || len(res.Rows) == 0 || res.Rows[0].Group[0] != want[i].group {
			t.Fatalf("panel %d analyze: %d %s", i, code, body)
		}
		if want[i].count > 0 && res.Rows[0].Values[0] != want[i].count {
			t.Fatalf("panel %d count = %v, want %v", i, res.Rows[0].Values[0], want[i].count)
		}
	}

	// Reorder, rename, then the shared range and refresh interval.
	order := fmt.Sprintf(`{"panelIds":[%d,%d,%d]}`, got.Panels[2].ID, got.Panels[0].ID, got.Panels[1].ID)
	if code, body = filterReq(t, srv, token, "PUT", fmt.Sprintf("/api/v1/boards/%d/panels/order", board), order); code != 200 {
		t.Fatalf("reorder: %d %s", code, body)
	}
	if code, body = filterReq(t, srv, token, "PUT", fmt.Sprintf("/api/v1/boards/%d/panels/%d", board, got.Panels[0].ID), `{"title":"Errors","view":"chart"}`); code != 200 {
		t.Fatalf("update panel: %d %s", code, body)
	}
	if code, body = filterReq(t, srv, token, "PUT", fmt.Sprintf("/api/v1/boards/%d", board), `{"name":"Overview","timeRange":"7d","refreshSeconds":300}`); code != 200 {
		t.Fatalf("update board: %d %s", code, body)
	}
	_, body = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/boards/%d", board), "")
	got = boardJSON{}
	_ = json.Unmarshal(body, &got)
	if got.TimeRange != "7d" || got.RefreshSeconds != 300 || got.Panels[0].Title != "Count by agent" || got.Panels[1].Title != "Errors" || got.Panels[1].View != "chart" {
		t.Fatalf("after edits = %s", body)
	}

	if code, _ = filterReq(t, srv, token, "DELETE", fmt.Sprintf("/api/v1/boards/%d/panels/%d", board, got.Panels[0].ID), ""); code != 200 {
		t.Fatalf("delete panel = %d", code)
	}
	if code, _ = filterReq(t, srv, token, "DELETE", fmt.Sprintf("/api/v1/boards/%d", board), ""); code != 200 {
		t.Fatalf("delete board = %d", code)
	}
	if code, _ = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/boards/%d", board), ""); code != 404 {
		t.Fatalf("get deleted board = %d, want 404", code)
	}
}

func TestBoardErrorsMapToStatuses(t *testing.T) {
	srv, sm, _ := boardServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	code, body := filterReq(t, srv, token, "POST", "/api/v1/boards", `{"projectId":1,"name":"b"}`)
	board := createdID(t, code, body)

	cases := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/boards", `{"projectId":1,"name":""}`, 400},
		{"POST", "/api/v1/boards", `{not json`, 400},
		{"POST", "/api/v1/boards", `{"projectId":1,"name":"b","timeRange":"1y"}`, 400},
		{"GET", "/api/v1/boards/abc", ``, 400},
		{"GET", "/api/v1/boards/999", ``, 404},
		{"PUT", "/api/v1/boards/999", `{"name":"b","timeRange":"24h"}`, 404},
		{"DELETE", "/api/v1/boards/999", ``, 404},
		{"POST", fmt.Sprintf("/api/v1/boards/%d/panels", board), `{"view":"table","definition":{"calcs":[]}}`, 400},
		{"POST", fmt.Sprintf("/api/v1/boards/%d/panels", board), `{"view":"table","filters":{"filters":[{"key":"","op":"="}]},"definition":{"calcs":["count"]}}`, 400},
		{"POST", "/api/v1/boards/999/panels", `{"view":"table","definition":{"calcs":["count"]}}`, 404},
		{"PUT", fmt.Sprintf("/api/v1/boards/%d/panels/order", board), `{"panelIds":[5]}`, 400},
		{"PUT", fmt.Sprintf("/api/v1/boards/%d/panels/77", board), `{"title":"x","view":"table"}`, 404},
		{"DELETE", fmt.Sprintf("/api/v1/boards/%d/panels/77", board), ``, 404},
	}
	for _, c := range cases {
		if got, body := filterReq(t, srv, token, c.method, c.path, c.body); got != c.want {
			t.Errorf("%s %s %q = %d, want %d (%s)", c.method, c.path, c.body, got, c.want, body)
		}
	}
}

func TestReleaseMarkerEndpoints(t *testing.T) {
	srv, sm, _ := boardServer(t)
	token, _, _ := sm.Create("admin", "local", nil)
	at := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	code, body := filterReq(t, srv, token, "POST", "/api/v1/releases",
		fmt.Sprintf(`{"projectId":1,"version":"v0.4.0","releasedAt":%q}`, at.Format(time.RFC3339)))
	createdID(t, code, body)
	if code, _ = filterReq(t, srv, token, "POST", "/api/v1/releases", `{"projectId":1,"version":""}`); code != 400 {
		t.Fatalf("empty version = %d, want 400", code)
	}

	code, body = filterReq(t, srv, token, "GET", "/api/v1/releases?"+filterRange().Encode(), "")
	var rels []struct {
		Version    string
		ReleasedAt time.Time
	}
	if code != 200 || json.Unmarshal(body, &rels) != nil || len(rels) != 1 || rels[0].Version != "v0.4.0" || !rels[0].ReleasedAt.Equal(at) {
		t.Fatalf("releases: %d %s", code, body)
	}
	// An empty range is an empty list, a missing one is rejected.
	q := url.Values{"project_id": {"1"}, "from": {at.Add(time.Hour).Format(time.RFC3339)}, "to": {at.Add(2 * time.Hour).Format(time.RFC3339)}}
	if _, body = filterReq(t, srv, token, "GET", "/api/v1/releases?"+q.Encode(), ""); strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("empty range = %s, want []", body)
	}
	if code, _ = filterReq(t, srv, token, "GET", "/api/v1/releases?project_id=1", ""); code != 400 {
		t.Fatalf("no range = %d, want 400", code)
	}
}
