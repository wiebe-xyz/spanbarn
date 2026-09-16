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

func TestMetricsRetentionDaysDefault(t *testing.T) {
	if got := Load().Retention.MetricsDays; got != 7 {
		t.Fatalf("default Retention.MetricsDays = %d, want 7", got)
	}
}

func TestMetricsRetentionDaysEnvOverride(t *testing.T) {
	t.Setenv("SPANBARN_METRICS_RETENTION_DAYS", "30")
	if got := Load().Retention.MetricsDays; got != 30 {
		t.Fatalf("Retention.MetricsDays = %d, want 30", got)
	}
}

func TestRollupCompactionEnabledByDefault(t *testing.T) {
	if !Load().Retention.CompactEnabled {
		t.Fatal("rollup compaction should be on by default")
	}
}

// TestRollupCompactionDisabledByEnv guards the off position of the switch. It
// was first wired through getenvInt, which only accepts values above zero and so
// read "0" as unset and handed back the default. The flag was set to 0 on
// production and compaction carried on regardless.
func TestRollupCompactionDisabledByEnv(t *testing.T) {
	for _, off := range []string{"0", "false", "no"} {
		t.Run(off, func(t *testing.T) {
			t.Setenv("SPANBARN_ROLLUP_COMPACT_ENABLED", off)
			if Load().Retention.CompactEnabled {
				t.Errorf("SPANBARN_ROLLUP_COMPACT_ENABLED=%s must disable compaction", off)
			}
		})
	}
}

func TestTraceBufferLimitsDefault(t *testing.T) {
	cfg := Load()
	if cfg.TraceBufferMaxSpans != 50000 {
		t.Errorf("TraceBufferMaxSpans = %d, want 50000", cfg.TraceBufferMaxSpans)
	}
	if cfg.TraceBufferTTLSeconds != 600 {
		t.Errorf("TraceBufferTTLSeconds = %d, want 600", cfg.TraceBufferTTLSeconds)
	}
}

// TestTraceBufferLimitsEnvOverride pins that the trace buffer's ceiling is
// tunable at all. The defaults imply a fixed ingest ceiling across every
// project; a deployment that outgrows it needs to raise the cap without waiting
// for a release, which is how a month of cron traces went missing.
func TestTraceBufferLimitsEnvOverride(t *testing.T) {
	t.Setenv("SPANBARN_TRACE_BUFFER_MAX_SPANS", "250000")
	t.Setenv("SPANBARN_TRACE_BUFFER_TTL_SECONDS", "120")
	cfg := Load()
	if cfg.TraceBufferMaxSpans != 250000 {
		t.Errorf("TraceBufferMaxSpans = %d, want 250000", cfg.TraceBufferMaxSpans)
	}
	if cfg.TraceBufferTTLSeconds != 120 {
		t.Errorf("TraceBufferTTLSeconds = %d, want 120", cfg.TraceBufferTTLSeconds)
	}
}
