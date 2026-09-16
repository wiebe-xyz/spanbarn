package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIngestBaseURL(t *testing.T) {
	cases := []struct {
		name      string
		host      string
		fwdHost   string
		trust     bool
		publicURL string
		want      string
	}{
		{name: "branded alias", host: "sb.acme.nl", want: "https://sb.acme.nl"},
		{name: "branded alias with port and caps", host: "SB.Acme.NL:443", want: "https://sb.acme.nl"},
		{name: "branded alias on a sub-zone", host: "sb.eu.acme.com", want: "https://sb.eu.acme.com"},
		{name: "canonical host", host: "spanbarn.wiebe.xyz", want: defaultPublicURL},
		{name: "canonical host with configured public URL", host: "spanbarn.staging.wiebe.xyz", publicURL: "https://spanbarn.staging.wiebe.xyz/", want: "https://spanbarn.staging.wiebe.xyz"},
		{name: "bare sb label", host: "sb", want: defaultPublicURL},
		{name: "sb without a TLD", host: "sb.localhost", want: defaultPublicURL},
		{name: "sb not the first label", host: "api.sb.acme.nl", want: defaultPublicURL},
		{name: "look-alike prefix", host: "sbx.acme.nl", want: defaultPublicURL},
		{name: "injection attempt", host: "sb.acme.nl/evil", want: defaultPublicURL},
		{name: "forwarded host ignored when proxy untrusted", host: "spanbarn.wiebe.xyz", fwdHost: "sb.acme.nl", want: defaultPublicURL},
		{name: "forwarded host used when proxy trusted", host: "spanbarn-writer:8080", fwdHost: "sb.acme.nl", trust: true, want: "https://sb.acme.nl"},
		{name: "forwarded host list takes the first entry", host: "x", fwdHost: "sb.acme.nl, sb.other.nl", trust: true, want: "https://sb.acme.nl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := trustProxyHeaders.Load()
			SetTrustProxy(tc.trust)
			t.Cleanup(func() { SetTrustProxy(prev) })

			req := httptest.NewRequest(http.MethodGet, "/api/v1/setup/x", nil)
			req.Host = tc.host
			if tc.fwdHost != "" {
				req.Header.Set("X-Forwarded-Host", tc.fwdHost)
			}
			s := &Server{publicURL: tc.publicURL}
			if got := s.ingestBaseURL(req); got != tc.want {
				t.Errorf("ingestBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHandleSetupKeepsBrandedHost is the onboarding guide end to end: requested
// on a branded alias, every ingest link names that alias, while the dashboard
// links stay on the canonical host.
func TestHandleSetupKeepsBrandedHost(t *testing.T) {
	repo := newTestRepo(t)
	s := &Server{logger: slog.Default(), repo: repo, sessionSecret: "test-secret"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/setup/acme", nil)
	req.Host = "sb.acme.nl"
	req.SetPathValue("slug", "acme")
	rec := httptest.NewRecorder()
	s.handleSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"| Endpoint   | https://sb.acme.nl/v1/traces |",
		"| Setup URL  | https://sb.acme.nl/api/v1/setup/acme |",
		"export OTEL_EXPORTER_OTLP_ENDPOINT=https://sb.acme.nl/\n",
		"curl -s -X POST '" + defaultPublicURL + "/api/v1/projects/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setup guide is missing %q", want)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "spanbarn.wiebe.xyz") && !strings.Contains(line, "/api/v1/projects/") {
			t.Errorf("branded guide leaks the canonical host outside dashboard links: %q", line)
		}
	}
}

func TestHandleSetupCanonicalHost(t *testing.T) {
	repo := newTestRepo(t)
	s := &Server{logger: slog.Default(), repo: repo, sessionSecret: "test-secret", publicURL: "https://spanbarn.test.wiebe.xyz"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/setup/acme", nil)
	req.Host = "spanbarn.test.wiebe.xyz"
	req.SetPathValue("slug", "acme")
	rec := httptest.NewRecorder()
	s.handleSetup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if want := "| Endpoint   | https://spanbarn.test.wiebe.xyz/v1/traces |"; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("setup guide is missing %q", want)
	}
}
