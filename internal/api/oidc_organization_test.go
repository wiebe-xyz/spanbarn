package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

const testOrgOther = "org_other"

func TestOIDCCallbackChecksOrganization(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{"owner of the organization", map[string]any{"organization_id": testOrgHome}, http.StatusFound},
		{"owner of another organization", map[string]any{"organization_id": testOrgOther}, http.StatusForbidden},
		{"token with no organization", map[string]any{"organization_id": ""}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp := newFakeIdP(t)
			svc, _ := newTestSessions(t)
			s := &Server{logger: slog.Default(), oidc: idp.client(), sessions: svc}
			tc.claims["nonce"] = "nonce-1"
			idp.script(0, "at-1", "rt-1", tc.claims)

			rec := httptest.NewRecorder()
			s.handleOIDCCallback(rec, callbackRequest("state-1", oauth2.GenerateVerifier(), "nonce-1"))

			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden {
				assertRefusal(t, rec)
			}
		})
	}
}

// A refused callback tells the user how to get in and starts no session.
func assertRefusal(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if !strings.Contains(rec.Body.String(), "Switch to your home organization") {
		t.Errorf("refusal does not say to switch organization: %s", rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "session" && c.Value != "" {
			t.Error("refused request set a session cookie")
		}
	}
}

func TestOIDCCallbackRefusesWhenOrganizationUnset(t *testing.T) {
	idp := newFakeIdP(t)
	svc, _ := newTestSessions(t)
	cfg := idp.client().Config()
	cfg.RequiredOrganizationID = ""
	s := &Server{logger: slog.Default(), oidc: auth.NewOIDCClient(cfg), sessions: svc}
	idp.script(0, "at-1", "rt-1", map[string]any{"nonce": "nonce-1"})

	rec := httptest.NewRecorder()
	s.handleOIDCCallback(rec, callbackRequest("state-1", oauth2.GenerateVerifier(), "nonce-1"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// IamBarn's refresh grant returns no id_token, so a refresh falls back to the
// claims stored at sign-in.
func TestSessionRefreshChecksOrganization(t *testing.T) {
	cases := []struct {
		name     string
		stored   string
		omitID   bool
		refresh  map[string]any
		wantCode int
	}{
		{name: "stored organization matches", stored: testOrgHome, omitID: true, wantCode: http.StatusOK},
		{name: "stored from another organization", stored: testOrgOther, omitID: true, wantCode: http.StatusUnauthorized},
		{name: "stored before the check existed", stored: "", omitID: true, wantCode: http.StatusUnauthorized},
		{
			name: "fresh id_token matches", stored: testOrgHome,
			refresh: map[string]any{"organization_id": testOrgHome}, wantCode: http.StatusOK,
		},
		{
			name: "fresh id_token from another organization", stored: testOrgHome,
			refresh: map[string]any{"organization_id": testOrgOther}, wantCode: http.StatusUnauthorized,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idp, svc, _, handler := sessionTestStack(t)
			idp.script(0, "at-2", "rt-2", tc.refresh)
			idp.mu.Lock()
			idp.omitIDToken = tc.omitID
			idp.mu.Unlock()

			token, _, err := svc.Create("Wiebe", "oidc", &OIDCSessionData{
				Claims: auth.OIDCClaims{
					Subject: "sub-1", SessionID: "sid-1", Roles: []string{"owner"}, OrganizationID: tc.stored,
				},
				AccessToken:     "at-login",
				RefreshToken:    "rt-1",
				AccessExpiresAt: time.Now().Add(-time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}

			if rec := doSession(handler, token); rec.Code != tc.wantCode {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// Every bearer sign-in reaches the API through SessionOrReadKey: sb's device-code
// login, its client-credentials login, and sb mcp, which is a local client of
// the same read routes.
func TestBearerSignInChecksOrganization(t *testing.T) {
	sm, _ := newTestSessions(t)
	oc, sign, issuer := newTestOIDC(t, "spanbarn-cli")
	handler := SessionOrReadKey(sm, nil, func() *auth.OIDCClient { return oc })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	base := func(org string) map[string]any {
		return map[string]any{
			"iss": issuer, "aud": []string{"spanbarn-cli"}, "token_use": "access_token",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"organization_id": org,
		}
	}
	deviceCode := func(org string) map[string]any {
		c := base(org)
		c["sub"], c["preferred_username"], c["roles"] = "u1", "wiebe", []string{"owner"}
		return c
	}
	clientCredentials := func(org string) map[string]any {
		c := base(org)
		c["sub"], c["client_id"], c["client_name"] = "ibc_m2m", "ibc_m2m", "ci"
		c["grant_type"], c["roles"] = "client_credentials", []string{"operator"}
		return c
	}

	paths := []struct {
		name   string
		path   string
		claims func(string) map[string]any
	}{
		{"device code", "/api/v1/traces", deviceCode},
		{"client credentials", "/api/v1/traces", clientCredentials},
		{"mcp read", "/api/v1/services", deviceCode},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			for org, want := range map[string]int{
				testOrgHome:  http.StatusOK,
				testOrgOther: http.StatusForbidden,
				"":           http.StatusForbidden,
			} {
				req := httptest.NewRequest(http.MethodGet, p.path, nil)
				req.Header.Set("Authorization", "Bearer "+sign(p.claims(org)))
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != want {
					t.Errorf("organization %q: status %d, want %d", org, rec.Code, want)
				}
				if want == http.StatusForbidden {
					assertRefusal(t, rec)
				}
			}
		})
	}
}

// A machine token in the right organization still needs SpanBarn as its audience
// and a role on the list.
func TestMachineTokenInTheOrganizationNeedsAudienceAndRole(t *testing.T) {
	sm, _ := newTestSessions(t)
	oc, sign, issuer := newTestOIDC(t, "spanbarn-cli")
	handler := SessionOrReadKey(sm, nil, func() *auth.OIDCClient { return oc })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	for _, tc := range []struct {
		name string
		aud  string
		want int
	}{
		{"another audience", "ibc_other_resource", http.StatusUnauthorized},
		{"role off the list", "spanbarn-cli", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/traces", nil)
			req.Header.Set("Authorization", "Bearer "+sign(map[string]any{
				"iss": issuer, "aud": []string{tc.aud}, "token_use": "access_token",
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
				"sub": "imc_machine", "client_id": "imc_machine", "grant_type": "client_credentials",
				"organization_id": testOrgHome, "roles": []string{"admin"},
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
