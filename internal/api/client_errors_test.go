package api

import (
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
