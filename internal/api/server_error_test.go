package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteServerErrorMapsCanceledTo499(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/attributes", nil)
	w := httptest.NewRecorder()
	writeServerError(w, r, "query failed", fmt.Errorf("list attributes: %w", context.Canceled))
	if w.Code != statusClientClosedRequest {
		t.Fatalf("status = %d, want %d", w.Code, statusClientClosedRequest)
	}
}

func TestWriteServerErrorKeeps500ForRealFailures(t *testing.T) {
	for _, err := range []error{errors.New("disk on fire"), context.DeadlineExceeded} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/attributes", nil)
		w := httptest.NewRecorder()
		writeServerError(w, r, "query failed", err)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("%v: status = %d, want 500", err, w.Code)
		}
	}
}
