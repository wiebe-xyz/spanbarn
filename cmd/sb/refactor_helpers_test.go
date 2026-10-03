package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPollSettings(t *testing.T) {
	now := time.Unix(1000, 0)
	interval, deadline := deviceAuthorization{}.pollSettings(now)
	if interval != 5 || !deadline.Equal(now.Add(10*time.Minute)) {
		t.Errorf("defaults: interval=%d deadline=%v", interval, deadline)
	}
	interval, deadline = deviceAuthorization{Interval: 2, ExpiresIn: 60}.pollSettings(now)
	if interval != 2 || !deadline.Equal(now.Add(time.Minute)) {
		t.Errorf("explicit: interval=%d deadline=%v", interval, deadline)
	}
}

func TestHTTPStatusErrorTruncates(t *testing.T) {
	err := httpStatusError(500, []byte("  "+strings.Repeat("x", 400)+"  "))
	want := "HTTP 500: " + strings.Repeat("x", 300)
	if err.Error() != want {
		t.Errorf("got %q", err.Error())
	}
}

func TestSetAuthHeaderPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantKey    string
		wantBearer string
	}{
		{"api key wins", Config{APIKey: "k", AccessToken: "a", SessionToken: "s"}, "k", ""},
		{"access token next", Config{AccessToken: "a", SessionToken: "s"}, "", "Bearer a"},
		{"session last", Config{SessionToken: "s"}, "", "Bearer s"},
		{"none", Config{}, "", ""},
	}
	for _, tc := range tests {
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		(&Client{cfg: tc.cfg}).setAuthHeader(req)
		if got := req.Header.Get("X-SpanBarn-Api-Key"); got != tc.wantKey {
			t.Errorf("%s: api key header = %q", tc.name, got)
		}
		if got := req.Header.Get("Authorization"); got != tc.wantBearer {
			t.Errorf("%s: authorization = %q", tc.name, got)
		}
	}
}

func TestReauthenticateWithoutCredentials(t *testing.T) {
	c := &Client{cfg: Config{}}
	if c.reauthenticate() {
		t.Error("reauthenticate must fail without OIDC or password credentials")
	}
}

func TestRunExitCodes(t *testing.T) {
	if code := run(nil); code != 1 {
		t.Errorf("no args: %d", code)
	}
	if code := run([]string{"version"}); code != 0 {
		t.Errorf("version: %d", code)
	}
	if code := run([]string{"help"}); code != 0 {
		t.Errorf("help: %d", code)
	}
	if code := run([]string{"no-such-command"}); code != 1 {
		t.Errorf("unknown: %d", code)
	}
}
