package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lastHTTPLine returns the fields of the final "http" log line.
func lastHTTPLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatalf("decode log line %q: %v", lines[len(lines)-1], err)
	}
	return got
}

func TestAccessLogCarriesCallerKindAndOrganization(t *testing.T) {
	_, svc, _, _ := sessionTestStack(t)
	oidcToken := newOIDCSessionRow(t, svc, time.Now().Add(time.Hour), "rt")
	localToken, _, err := svc.Create("alice", "local", nil)
	if err != nil {
		t.Fatalf("create local session: %v", err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := loggingMiddleware(logger, func(*http.Request, int, time.Duration) {}, SessionMiddleware(svc)(ok))

	call := func(token string) map[string]any {
		buf.Reset()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "session", Value: token})
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return lastHTTPLine(t, &buf)
	}

	line := call(oidcToken)
	if line["auth_kind"] != "oidc_session" || line["org_id"] != testOrgHome {
		t.Fatalf("oidc session line = %v, want auth_kind oidc_session and org_id %s", line, testOrgHome)
	}
	line = call(localToken)
	if line["auth_kind"] != "local_session" {
		t.Fatalf("local session auth_kind = %v", line["auth_kind"])
	}
	if _, has := line["org_id"]; has {
		t.Fatalf("local session line carries an org_id: %v", line)
	}
	line = call("")
	if _, has := line["auth_kind"]; has {
		t.Fatalf("unauthenticated line carries auth_kind: %v", line)
	}
	for _, v := range line {
		if s, isStr := v.(string); isStr && (strings.Contains(s, oidcToken) || strings.Contains(s, "alice")) {
			t.Fatalf("log line leaks a token or a user: %v", line)
		}
	}
}

func TestSessionOrganizationReadsOnlyTheOrganizationClaim(t *testing.T) {
	cases := map[string]string{
		`{"organization_id":"org_a","email":"x@y"}`: "org_a",
		`{"email":"x@y"}`: "",
		``:                "",
		`not json`:        "",
	}
	for in, want := range cases {
		if got := sessionOrganization(in); got != want {
			t.Errorf("sessionOrganization(%q) = %q, want %q", in, got, want)
		}
	}
}
