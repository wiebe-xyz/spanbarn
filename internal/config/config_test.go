package config

import "testing"

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
