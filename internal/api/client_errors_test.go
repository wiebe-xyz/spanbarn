package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleClientErrorTruncatesAndAccepts(t *testing.T) {
	s := &Server{logger: slog.Default()}
	body := `{"message":"` + strings.Repeat("a", 5000) + `","type":"TypeError","stack":"` + strings.Repeat("b", 20000) + `"}`
	rec := httptest.NewRecorder()
	s.handleClientError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("héllo", 2); got != "h" {
		t.Fatalf("expected cut at rune boundary, got %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("unexpected %q", got)
	}
}

func TestClientErrorAttributesAreRecordedScrubbedAndCapped(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	body := `{"message":"Lazy chunk still missing after a reload","type":"StaleChunk","attributes":{` +
		`"chunk":"https://spanbarn.test/assets/TraceDetailPage-abc.css","authToken":"s3cret","long":"` + strings.Repeat("x", 2000) + `"}}`
	rec := httptest.NewRecorder()
	s.handleClientError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", strings.NewReader(body)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	logged := buf.String()
	if !strings.Contains(logged, "TraceDetailPage-abc.css") {
		t.Fatalf("chunk URL not recorded: %s", logged)
	}
	if strings.Contains(logged, "s3cret") || !strings.Contains(logged, "[redacted]") {
		t.Fatalf("sensitive value not scrubbed: %s", logged)
	}
	if strings.Contains(logged, strings.Repeat("x", maxClientErrorAttrValue+1)) {
		t.Fatalf("long value not capped")
	}
}

func TestClientErrorAttributesCapCount(t *testing.T) {
	in := map[string]string{}
	for i := 0; i < 40; i++ {
		in[strings.Repeat("k", i+1)] = "v"
	}
	if got := len(clientErrorAttributes(in)); got != maxClientErrorAttrs {
		t.Fatalf("kept %d attributes, want %d", got, maxClientErrorAttrs)
	}
	if clientErrorAttributes(nil) != nil {
		t.Fatalf("empty input should give nil")
	}
}

func TestClientErrorGroupingNamesMethodRouteAndStatus(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	post := func(body string) {
		rec := httptest.NewRecorder()
		s.handleClientError(rec, httptest.NewRequest(http.MethodPost, "/api/v1/client-errors", strings.NewReader(body)))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", rec.Code)
		}
	}
	post(`{"message":"HTTP 503 GET /api/v1/dashboard/counts?from=2026-11-26T20%3A32%3A24.315Z&to=x","type":"Error","attributes":{"type":"http_error","status":"503"}}`)
	logged := buf.String()
	for _, want := range []string{`"http.method":"GET"`, `"http.route":"/api/v1/dashboard/counts"`, `"http.status_code":"503"`} {
		if !strings.Contains(logged, want) {
			t.Errorf("missing %s in %s", want, logged)
		}
	}
	if strings.Contains(logged, `"http.route":"/api/v1/dashboard/counts?`) {
		t.Errorf("route carries the query string: %s", logged)
	}

	buf.Reset()
	post(`{"message":"HTTP 503 GET /api/v1/x","type":"Error","attributes":{"type":"unhandledrejection"}}`)
	post(`{"message":"boom","type":"Error","attributes":{"type":"http_error"}}`)
	if strings.Contains(buf.String(), "http.route") {
		t.Errorf("a report that is not a parsed http_error got grouping attributes: %s", buf.String())
	}
}
