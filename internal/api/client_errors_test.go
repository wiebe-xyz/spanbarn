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
