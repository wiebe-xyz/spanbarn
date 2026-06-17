package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the global CLI config stored at ~/.config/spanbarn/cli.json
// (override with SB_CONFIG). Several auth methods are supported, tried in this
// order of precedence: API key, then OIDC access token, then session.
//   - read-scoped API key (X-SpanBarn-Api-Key), or
//   - IamBarn OIDC login (device-code or client-credentials) yielding an access
//     token sent as Authorization: Bearer; refreshed/re-fetched on expiry, or
//   - username/password login (POST /api/v1/login) yielding a session token,
//     also sent as Authorization: Bearer.
type Config struct {
	URL     string `json:"url"`
	Project string `json:"project,omitempty"` // default project slug

	APIKey string `json:"api_key,omitempty"`

	// Local password session.
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	SessionToken string `json:"session_token,omitempty"`

	// IamBarn OIDC. AuthType is "oidc-device" or "oidc-m2m" when in use.
	AuthType         string `json:"auth_type,omitempty"`
	OIDCIssuer       string `json:"oidc_issuer,omitempty"`
	OIDCClientID     string `json:"oidc_client_id,omitempty"`
	OIDCClientSecret string `json:"oidc_client_secret,omitempty"` // m2m only
	AccessToken      string `json:"access_token,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"` // device only
	TokenExpiry      int64  `json:"token_expiry,omitempty"`  // unix seconds
}

func configPath() string {
	if p := os.Getenv("SB_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "sb.json"
	}
	return filepath.Join(home, ".config", "spanbarn", "cli.json")
}

func loadConfig() (Config, error) {
	data, err := os.ReadFile(configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("not logged in — run: sb login --url URL (--api-key KEY | --username USER)")
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("corrupt config: %w", err)
	}
	if cfg.URL == "" {
		return Config{}, fmt.Errorf("no URL configured — run: sb login --url URL (--api-key KEY | --username USER)")
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}

// --- per-project local config (.spanbarn.json), discovered by walking up ---

const localConfigFile = ".spanbarn.json"

// LocalConfig is the per-project settings file checked into a repo so the CLI
// knows which SpanBarn project the working directory belongs to.
type LocalConfig struct {
	Project string `json:"project,omitempty"`
}

func findLocalConfig() (LocalConfig, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return LocalConfig{}, false
	}
	for {
		p := filepath.Join(dir, localConfigFile)
		if data, err := os.ReadFile(p); err == nil {
			var lc LocalConfig
			if json.Unmarshal(data, &lc) == nil && lc.Project != "" {
				return lc, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return LocalConfig{}, false
}

func saveLocalConfig(lc LocalConfig) error {
	data, err := json.MarshalIndent(lc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(localConfigFile, append(data, '\n'), 0644)
}

// resolveProject picks the project slug to use: explicit flag, then the
// walk-up .spanbarn.json, then the global config default.
func resolveProject(flag string, cfg Config) string {
	if flag != "" {
		return flag
	}
	if lc, ok := findLocalConfig(); ok && lc.Project != "" {
		return lc.Project
	}
	return cfg.Project
}
