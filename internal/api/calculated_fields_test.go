package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func TestCalculatedFieldsRequireASession(t *testing.T) {
	srv, _, _ := boardServer(t)
	for _, c := range [][2]string{
		{"GET", "/api/v1/calculated-fields?project_id=1"},
		{"POST", "/api/v1/calculated-fields"},
		{"POST", "/api/v1/calculated-fields/preview"},
		{"PUT", "/api/v1/calculated-fields/1"},
		{"DELETE", "/api/v1/calculated-fields/1"},
	} {
		if code, _ := filterReq(t, srv, "", c[0], c[1], ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", c[0], c[1], code)
		}
	}
}

func TestCalculatedFieldsValidationIs400(t *testing.T) {
	srv, sm, _ := boardServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]string{
		"shadows a column":   `{"projectId":1,"name":"duration_us","expression":"1"}`,
		"cycle":              `{"projectId":1,"name":"a","expression":"a + 1"}`,
		"oversized":          `{"projectId":1,"name":"a","expression":"` + strings.Repeat("1+", 400) + `1"}`,
		"unknown function":   `{"projectId":1,"name":"a","expression":"sleep(1)"}`,
		"sql in expression":  `{"projectId":1,"name":"a","expression":"1; DROP TABLE spans"}`,
		"sql in name":        `{"projectId":1,"name":"a; DROP TABLE spans","expression":"1"}`,
		"quote in name":      `{"projectId":1,"name":"a'b","expression":"1"}`,
		"missing project":    `{"name":"a","expression":"1"}`,
		"empty expression":   `{"projectId":1,"name":"a","expression":""}`,
		"deeply nested":      `{"projectId":1,"name":"a","expression":"` + strings.Repeat("(", 30) + "1" + strings.Repeat(")", 30) + `"}`,
		"attributes. prefix": `{"projectId":1,"name":"attributes.x","expression":"1"}`,
	}
	for name, body := range bodies {
		code, resp := filterReq(t, srv, token, "POST", "/api/v1/calculated-fields", body)
		if code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, code, resp)
		}
	}
	if code, _ := filterReq(t, srv, token, "POST", "/api/v1/calculated-fields", `{`); code != http.StatusBadRequest {
		t.Errorf("bad JSON = %d", code)
	}
	if code, _ := filterReq(t, srv, token, "PUT", "/api/v1/calculated-fields/999", `{"name":"a","expression":"1"}`); code != http.StatusNotFound {
		t.Errorf("update missing = %d, want 404", code)
	}
	if code, _ := filterReq(t, srv, token, "DELETE", "/api/v1/calculated-fields/999", ""); code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", code)
	}
	if code, _ := filterReq(t, srv, token, "DELETE", "/api/v1/calculated-fields/abc", ""); code != http.StatusBadRequest {
		t.Errorf("bad id = %d, want 400", code)
	}
}

// A field defined over the API drives a filter and a group-by, and returns the
// rows of the hand-written equivalent.
func TestCalculatedFieldEndToEnd(t *testing.T) {
	srv, sm, repo := boardServer(t)
	token, _, err := sm.Create("admin", "local", nil)
	if err != nil {
		t.Fatal(err)
	}
	var spans []repository.Span
	for i, dur := range []int64{500, 1500, 2500, 2600, 9000} {
		s := healthSpan(fmt.Sprintf("t%d", i), fmt.Sprintf("s%d", i), "", "op")
		s.Kind, s.Status, s.DurationUs = "server", "ok", dur
		s.Attributes = `{"url.path":"/a"}`
		spans = append(spans, s)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	code, body := filterReq(t, srv, token, "POST", "/api/v1/calculated-fields",
		`{"projectId":1,"name":"duration_ms","expression":"duration_us / 1000"}`)
	id := createdID(t, code, body)
	if code, body = filterReq(t, srv, token, "POST", "/api/v1/calculated-fields",
		`{"projectId":1,"name":"duration_ms","expression":"1"}`); code != http.StatusBadRequest {
		t.Fatalf("duplicate name: %d %s", code, body)
	}

	code, body = filterReq(t, srv, token, "GET", "/api/v1/calculated-fields?project_id=1", "")
	var list []struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Expression string `json:"expression"`
	}
	if code != 200 || json.Unmarshal(body, &list) != nil || len(list) != 1 || list[0].ID != id || list[0].Expression != "duration_us / 1000" {
		t.Fatalf("list: %d %s", code, body)
	}

	analyze := func(filterJSON string, groupBy string) map[string]float64 {
		q := filterRange()
		q.Set("calc", "count")
		if filterJSON != "" {
			q.Set("filter", filterJSON)
		}
		q.Set("group_by", groupBy)
		code, body := filterReq(t, srv, token, "GET", "/api/v1/analyze?"+q.Encode(), "")
		var res struct {
			Rows []struct {
				Group  []string
				Values []float64
			}
		}
		if code != 200 || json.Unmarshal(body, &res) != nil {
			t.Fatalf("analyze %s %s: %d %s", filterJSON, groupBy, code, body)
		}
		out := map[string]float64{}
		for _, r := range res.Rows {
			out[r.Group[0]] = r.Values[0]
		}
		return out
	}

	withField := analyze(`{"filters":[{"key":"duration_ms","op":">=","value":2}]}`, "url.path")
	byHand := analyze(`{"filters":[{"key":"duration_us","op":">=","value":2000}]}`, "url.path")
	if fmt.Sprint(withField) != fmt.Sprint(byHand) || withField["/a"] != 3 {
		t.Fatalf("filter: field %v, by hand %v", withField, byHand)
	}
	grouped := analyze("", "duration_ms")
	if grouped["0.5"] != 1 || grouped["1.5"] != 1 || grouped["9"] != 1 || len(grouped) != 5 {
		t.Fatalf("group-by: %v", grouped)
	}

	// An edit applies to the next query.
	if code, body = filterReq(t, srv, token, "PUT", fmt.Sprintf("/api/v1/calculated-fields/%d", id),
		`{"name":"duration_ms","expression":"duration_us / 500"}`); code != 200 {
		t.Fatalf("update: %d %s", code, body)
	}
	if got := analyze("", "duration_ms"); got["1"] != 1 || got["18"] != 1 {
		t.Fatalf("after update: %v", got)
	}

	// Preview evaluates the unsaved expression on a sample span.
	code, body = filterReq(t, srv, token, "POST", "/api/v1/calculated-fields/preview",
		`{"projectId":1,"expression":"if(duration_us > 2000, 'slow', 'fast')"}`)
	var prev struct {
		Samples []struct {
			SpanID string `json:"spanId"`
			Value  any    `json:"value"`
		} `json:"samples"`
	}
	if code != 200 || json.Unmarshal(body, &prev) != nil || len(prev.Samples) != 5 {
		t.Fatalf("preview: %d %s", code, body)
	}
	for _, s := range prev.Samples {
		if s.Value != "slow" && s.Value != "fast" {
			t.Fatalf("preview value %v", s.Value)
		}
	}
	if code, body = filterReq(t, srv, token, "POST", "/api/v1/calculated-fields/preview",
		`{"projectId":1,"expression":"nope("}`); code != http.StatusBadRequest {
		t.Fatalf("bad preview: %d %s", code, body)
	}

	// Deleting the field turns the key back into a plain attribute.
	if code, _ = filterReq(t, srv, token, "DELETE", fmt.Sprintf("/api/v1/calculated-fields/%d", id), ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if got := analyze("", "duration_ms"); len(got) != 1 || got[""] != 5 {
		t.Fatalf("after delete: %v", got)
	}
}
