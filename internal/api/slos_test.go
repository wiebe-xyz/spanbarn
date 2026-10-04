package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

const sloBody1 = `{"name":"Checkout availability","goodFilter":{"filters":[{"key":"status","op":"!=","value":"error"}]},` +
	`"totalFilter":{"filters":[{"key":"service","op":"=","value":"checkout"}]},"target":0.995,"windowDays":30}`

func sloTestServer(t *testing.T) (*Server, string, *repository.Repository) {
	t.Helper()
	srv, sm, repo := boardServer(t)
	if _, err := repo.CreateProject("p2", "P2"); err != nil {
		t.Fatal(err)
	}
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	return srv, token, repo
}

func TestSLOsRequireASession(t *testing.T) {
	srv, _, _ := sloTestServer(t)
	for _, c := range [][2]string{
		{"GET", "/api/v1/slos?project_id=1"}, {"POST", "/api/v1/slos?project_id=1"},
		{"GET", "/api/v1/slos/1/status?project_id=1"}, {"GET", "/api/v1/slos/1/burn-alerts?project_id=1"},
		{"DELETE", "/api/v1/slos/1/burn-alerts/1?project_id=1"},
	} {
		if code, _ := filterReq(t, srv, "", c[0], c[1], ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c[0], c[1], code)
		}
	}
}

func TestSLOCrudRoundTrip(t *testing.T) {
	srv, token, _ := sloTestServer(t)
	code, body := filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=1", sloBody1)
	id := createdID(t, code, body)

	if code, _ = filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=1", sloBody1); code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409", code)
	}

	code, body = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/slos/%d?project_id=1", id), "")
	var got struct {
		ID         int64           `json:"id"`
		ProjectID  int64           `json:"projectId"`
		Name       string          `json:"name"`
		GoodFilter json.RawMessage `json:"goodFilter"`
		Target     float64         `json:"target"`
		WindowDays int             `json:"windowDays"`
	}
	if code != 200 || json.Unmarshal(body, &got) != nil || got.ID != id || got.ProjectID != 1 ||
		got.Name != "Checkout availability" || got.Target != 0.995 || got.WindowDays != 30 {
		t.Fatalf("get: %d %s", code, body)
	}
	var good map[string]any
	if json.Unmarshal(got.GoodFilter, &good) != nil || good["filters"] == nil {
		t.Fatalf("good filter = %s", got.GoodFilter)
	}

	code, body = filterReq(t, srv, token, "GET", "/api/v1/slos?project_id=1", "")
	var list []struct{ ID int64 }
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list) != 1 {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body = filterReq(t, srv, token, "GET", "/api/v1/slos?project_id=2", ""); code != 200 || string(body) != "[]\n" {
		t.Fatalf("empty list: %d %q", code, body)
	}

	upd := `{"name":"Renamed","target":0.99,"windowDays":7}`
	if code, body = filterReq(t, srv, token, "PUT", fmt.Sprintf("/api/v1/slos/%d?project_id=1", id), upd); code != 200 {
		t.Fatalf("update: %d %s", code, body)
	}
	_, body = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/slos/%d?project_id=1", id), "")
	if json.Unmarshal(body, &got) != nil || got.Name != "Renamed" || got.WindowDays != 7 {
		t.Fatalf("after update: %s", body)
	}

	if code, _ = filterReq(t, srv, token, "DELETE", fmt.Sprintf("/api/v1/slos/%d?project_id=1", id), ""); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ = filterReq(t, srv, token, "GET", fmt.Sprintf("/api/v1/slos/%d?project_id=1", id), ""); code != 404 {
		t.Fatalf("get after delete = %d", code)
	}
}

func TestSLOValidationIs400(t *testing.T) {
	srv, token, _ := sloTestServer(t)
	for name, body := range map[string]string{
		"empty name":    `{"name":"","target":0.99,"windowDays":30}`,
		"target 1":      `{"name":"a","target":1,"windowDays":30}`,
		"window 91":     `{"name":"a","target":0.99,"windowDays":91}`,
		"bad filter":    `{"name":"a","target":0.99,"windowDays":30,"goodFilter":{"filters":[{"key":"status","op":"~"}]}}`,
		"invalid JSON":  `{"name":`,
		"unknown field": `{"name":"a","target":0.99,"windowDays":30,"goodFilter":{"bogus":true}}`,
	} {
		if code, b := filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=1", body); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, code, b)
		}
	}
	for _, p := range []string{"/api/v1/slos", "/api/v1/slos?project_id=0", "/api/v1/slos/1/status"} {
		if code, _ := filterReq(t, srv, token, "GET", p, ""); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 without a project", p, code)
		}
	}
	if code, _ := filterReq(t, srv, token, "GET", "/api/v1/slos/abc?project_id=1", ""); code != http.StatusBadRequest {
		t.Errorf("non-numeric id = %d, want 400", code)
	}
}

func TestSLOBurnAlertCrudRoundTrip(t *testing.T) {
	srv, token, _ := sloTestServer(t)
	_, body := filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=1", sloBody1)
	var created struct{ ID int64 }
	_ = json.Unmarshal(body, &created)
	base := fmt.Sprintf("/api/v1/slos/%d/burn-alerts", created.ID)

	code, body := filterReq(t, srv, token, "POST", base+"?project_id=1",
		`{"windowMinutes":60,"burnRate":14.4,"webhookUrl":"https://hooks.example.com/x"}`)
	alertID := createdID(t, code, body)

	for name, b := range map[string]string{
		"window equals SLO window": `{"windowMinutes":43200,"burnRate":2}`,
		"zero burn":                `{"windowMinutes":60,"burnRate":0}`,
		"ftp webhook":              `{"windowMinutes":60,"burnRate":2,"webhookUrl":"ftp://x/y"}`,
	} {
		if code, _ = filterReq(t, srv, token, "POST", base+"?project_id=1", b); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}

	code, body = filterReq(t, srv, token, "GET", base+"?project_id=1", "")
	var alerts []struct {
		ID              int64   `json:"id"`
		WindowMinutes   int     `json:"windowMinutes"`
		BurnRate        float64 `json:"burnRate"`
		CooldownMinutes int     `json:"cooldownMinutes"`
		Enabled         bool    `json:"enabled"`
	}
	if code != 200 || json.Unmarshal(body, &alerts) != nil || len(alerts) != 1 || alerts[0].ID != alertID ||
		alerts[0].WindowMinutes != 60 || !alerts[0].Enabled || alerts[0].CooldownMinutes != 30 {
		t.Fatalf("list: %d %s", code, body)
	}

	item := fmt.Sprintf("%s/%d?project_id=1", base, alertID)
	if code, body = filterReq(t, srv, token, "PUT", item, `{"windowMinutes":30,"burnRate":6,"enabled":false,"cooldownMinutes":5}`); code != 200 {
		t.Fatalf("update: %d %s", code, body)
	}
	_, body = filterReq(t, srv, token, "GET", base+"?project_id=1", "")
	if json.Unmarshal(body, &alerts) != nil || alerts[0].WindowMinutes != 30 || alerts[0].Enabled || alerts[0].CooldownMinutes != 5 {
		t.Fatalf("after update: %s", body)
	}
	if code, _ = filterReq(t, srv, token, "DELETE", item, ""); code != 200 {
		t.Fatalf("delete = %d", code)
	}
	if code, _ = filterReq(t, srv, token, "DELETE", item, ""); code != 404 {
		t.Fatalf("second delete = %d, want 404", code)
	}
}

func TestSLOStatusEndpoint(t *testing.T) {
	srv, token, repo := sloTestServer(t)
	_, body := filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=1", sloBody1)
	var created struct{ ID int64 }
	_ = json.Unmarshal(body, &created)
	statusPath := fmt.Sprintf("/api/v1/slos/%d/status?project_id=1", created.ID)

	// No traffic: full budget, not an error.
	type status struct {
		Good, Total     int64
		BudgetRemaining float64
		Target          float64
		Alerts          []struct {
			ID          int64
			CurrentBurn float64
			Firing      bool
		}
	}
	var st status
	code, body := filterReq(t, srv, token, "GET", statusPath, "")
	if code != 200 || json.Unmarshal(body, &st) != nil || st.BudgetRemaining != 1 || st.Total != 0 || st.Target != 0.995 {
		t.Fatalf("empty status: %d %s", code, body)
	}

	code, body = filterReq(t, srv, token, "POST", fmt.Sprintf("/api/v1/slos/%d/burn-alerts?project_id=1", created.ID),
		`{"windowMinutes":60,"burnRate":5}`)
	alertID := createdID(t, code, body)

	// 1000 requests ten days ago with 3 bad, then 100 in the last hour with 5 bad.
	now := time.Now().UTC().Truncate(time.Minute)
	if err := repo.InsertSLOCounts([]repository.SLOCount{
		{SLOID: created.ID, BucketStart: now.AddDate(0, 0, -10), Good: 997, Total: 1000},
		{SLOID: created.ID, BucketStart: now.Add(-10 * time.Minute), Good: 95, Total: 100},
	}); err != nil {
		t.Fatal(err)
	}
	code, body = filterReq(t, srv, token, "GET", statusPath, "")
	st = status{}
	if code != 200 || json.Unmarshal(body, &st) != nil {
		t.Fatalf("status: %d %s", code, body)
	}
	// 1100 total, 8 bad against a 0.5% budget of 5.5.
	if st.Good != 1092 || st.Total != 1100 || math.Abs(st.BudgetRemaining-(1-8/5.5)) > 1e-9 {
		t.Fatalf("status = %s", body)
	}
	if len(st.Alerts) != 1 || st.Alerts[0].ID != alertID || math.Abs(st.Alerts[0].CurrentBurn-10) > 1e-9 {
		t.Fatalf("alerts = %s", body)
	}
}

// Project 1 cannot read, change or delete project 2's SLO or its alerts.
func TestSLOCrossProjectAccessIs404(t *testing.T) {
	srv, token, _ := sloTestServer(t)
	_, body := filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=2", sloBody1)
	var created struct{ ID int64 }
	_ = json.Unmarshal(body, &created)
	sloPath := fmt.Sprintf("/api/v1/slos/%d", created.ID)
	code, body := filterReq(t, srv, token, "POST", sloPath+"/burn-alerts?project_id=2", `{"windowMinutes":60,"burnRate":5}`)
	alertID := createdID(t, code, body)

	cases := []struct{ method, path, body string }{
		{"GET", sloPath + "?project_id=1", ""},
		{"PUT", sloPath + "?project_id=1", `{"name":"x","target":0.9,"windowDays":7}`},
		{"DELETE", sloPath + "?project_id=1", ""},
		{"GET", sloPath + "/status?project_id=1", ""},
		{"GET", sloPath + "/burn-alerts?project_id=1", ""},
		{"POST", sloPath + "/burn-alerts?project_id=1", `{"windowMinutes":60,"burnRate":5}`},
		{"PUT", fmt.Sprintf("%s/burn-alerts/%d?project_id=1", sloPath, alertID), `{"windowMinutes":30,"burnRate":2}`},
		{"DELETE", fmt.Sprintf("%s/burn-alerts/%d?project_id=1", sloPath, alertID), ""},
	}
	for _, c := range cases {
		if code, b := filterReq(t, srv, token, c.method, c.path, c.body); code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s, want 404", c.method, c.path, code, b)
		}
	}

	// Project 2 still has everything, untouched.
	for _, p := range []string{sloPath + "?project_id=2", sloPath + "/status?project_id=2"} {
		if code, b := filterReq(t, srv, token, "GET", p, ""); code != 200 {
			t.Errorf("GET %s = %d %s after the cross-project attempts", p, code, b)
		}
	}
	_, body = filterReq(t, srv, token, "GET", sloPath+"/burn-alerts?project_id=2", "")
	var alerts []struct{ ID int64 }
	if json.Unmarshal(body, &alerts) != nil || len(alerts) != 1 || alerts[0].ID != alertID {
		t.Fatalf("alerts of project 2 = %s", body)
	}

	// An alert reached through a different SLO of its own project is 404 as well.
	_, body = filterReq(t, srv, token, "POST", "/api/v1/slos?project_id=2", `{"name":"Other","target":0.9,"windowDays":7}`)
	var other struct{ ID int64 }
	_ = json.Unmarshal(body, &other)
	wrong := fmt.Sprintf("/api/v1/slos/%d/burn-alerts/%d?project_id=2", other.ID, alertID)
	if code, _ := filterReq(t, srv, token, "DELETE", wrong, ""); code != http.StatusNotFound {
		t.Errorf("alert through the wrong SLO = %d, want 404", code)
	}
}
