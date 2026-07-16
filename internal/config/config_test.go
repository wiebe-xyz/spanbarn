package config

import "testing"

func TestPostLogoutRedirectURIDefault(t *testing.T) {
	t.Setenv("SPANBARN_OIDC_REDIRECT_URL", "https://spanbarn.example.com/api/v1/oidc/callback")
	cfg := Load()
	want := "https://spanbarn.example.com/api/v1/oidc/logout-complete"
	if cfg.OIDC.PostLogoutRedirectURI != want {
		t.Fatalf("derived post-logout uri = %q, want %q", cfg.OIDC.PostLogoutRedirectURI, want)
	}
}

func TestPostLogoutRedirectURIExplicitWins(t *testing.T) {
	t.Setenv("SPANBARN_OIDC_REDIRECT_URL", "https://spanbarn.example.com/api/v1/oidc/callback")
	t.Setenv("SPANBARN_OIDC_POST_LOGOUT_REDIRECT_URI", "https://spanbarn.example.com/bye")
	cfg := Load()
	if cfg.OIDC.PostLogoutRedirectURI != "https://spanbarn.example.com/bye" {
		t.Fatalf("explicit post-logout uri not honoured: %q", cfg.OIDC.PostLogoutRedirectURI)
	}
}

func TestValidateSessionSecret(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		secret  string
		wantErr bool
	}{
		{"prod without secret", "production", "", true},
		{"staging without secret", "staging", "", true},
		{"testing without secret", "testing", "", true},
		{"prod with secret", "production", "s3cret", false},
		{"development without secret", "development", "", false},
		{"empty env without secret", "", "", false},
		{"local without secret", "local", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Environment: tc.env, SessionSecret: tc.secret}
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate(env=%q, secret=%q): want error, got nil", tc.env, tc.secret)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate(env=%q, secret=%q): want nil, got %v", tc.env, tc.secret, err)
			}
		})
	}
}

func TestIsDevEnvironment(t *testing.T) {
	dev := []string{"", "development", "DEV", "Local", "  dev  "}
	for _, e := range dev {
		if !(Config{Environment: e}).IsDevEnvironment() {
			t.Errorf("IsDevEnvironment(%q) = false, want true", e)
		}
	}
	nonDev := []string{"production", "staging", "testing"}
	for _, e := range nonDev {
		if (Config{Environment: e}).IsDevEnvironment() {
			t.Errorf("IsDevEnvironment(%q) = true, want false", e)
		}
	}
}

func TestRetentionInterestingHoursDefault(t *testing.T) {
	if got := Load().Retention.InterestingHours; got != 48 {
		t.Fatalf("default Retention.InterestingHours = %d, want 48", got)
	}
}

func TestRetentionInterestingHoursEnvOverride(t *testing.T) {
	t.Setenv("SPANBARN_RETENTION_INTERESTING_HOURS", "12")
	if got := Load().Retention.InterestingHours; got != 12 {
		t.Fatalf("Retention.InterestingHours = %d, want 12", got)
	}
}
