package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type fakeRejections struct{ rows []repository.KeyRejection }

func (f fakeRejections) ListKeyRejections(time.Time) ([]repository.KeyRejection, error) {
	return f.rows, nil
}

func keyRejectionRequest(t *testing.T, h http.Handler, method, target string, projectID int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req = req.WithContext(SetProjectID(req.Context(), projectID))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestKeyRejectionsEndpointReportsRevokedAndUnknownKeys(t *testing.T) {
	seen := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	h := &keyRejectionHandlers{svc: service.NewKeyRejectionService(fakeRejections{rows: []repository.KeyRejection{
		{Fingerprint: "1d4e4d34", Count: 12, FirstSeen: seen.Add(-time.Hour), LastSeen: seen, Revoked: true, KeyID: 207, ProjectID: 5, KeyName: "site", RevokedAt: seen.Add(-2 * time.Hour)},
		{Fingerprint: "ffffffff", Count: 1, FirstSeen: seen, LastSeen: seen},
	}})}

	rr := keyRejectionRequest(t, h, http.MethodGet, "/api/v1/apikey-rejections", 0)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body)
	}
	var got keyRejectionsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RetentionDays != 30 || len(got.Rejections) != 2 {
		t.Fatalf("response = %+v, want 30 days and two rows", got)
	}
	if r := got.Rejections[0]; r.Reason != "revoked" || r.KeyID != 207 || r.ProjectID != 5 || r.KeyName != "site" || r.Count != 12 || r.RevokedAt == "" {
		t.Errorf("revoked row = %+v", r)
	}
	if r := got.Rejections[1]; r.Reason != "unknown" || r.KeyID != 0 || r.KeyName != "" {
		t.Errorf("unknown row = %+v", r)
	}
}

func TestKeyRejectionsEndpointStaysClosedToProjectCallers(t *testing.T) {
	h := &keyRejectionHandlers{svc: service.NewKeyRejectionService(fakeRejections{})}
	cases := map[string]struct {
		method, target string
		project        int64
		want           int
	}{
		"a key bound to a project":     {http.MethodGet, "/api/v1/apikey-rejections", 7, http.StatusForbidden},
		"a request naming a project":   {http.MethodGet, "/api/v1/apikey-rejections?project_id=7", 0, http.StatusForbidden},
		"a method that writes":         {http.MethodPost, "/api/v1/apikey-rejections", 0, http.StatusMethodNotAllowed},
		"a since that is not a time":   {http.MethodGet, "/api/v1/apikey-rejections?since=yesterday", 0, http.StatusBadRequest},
		"a since inside the retention": {http.MethodGet, "/api/v1/apikey-rejections?since=2026-11-18T00:00:00Z", 0, http.StatusOK},
	}
	for name, c := range cases {
		if rr := keyRejectionRequest(t, h, c.method, c.target, c.project); rr.Code != c.want {
			t.Errorf("%s: status = %d, want %d", name, rr.Code, c.want)
		}
	}
}
