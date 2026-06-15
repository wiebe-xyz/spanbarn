package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the global CLI config stored at ~/.config/spanbarn/cli.json
// (override with SB_CONFIG). Two auth methods are supported:
//   - a read-scoped API key sent via the X-SpanBarn-Api-Key header, or
//   - username/password login (POST /api/v1/login) which yields a session
//     token sent as Authorization: Bearer. The password is stored so the CLI
//     can transparently re-authenticate when the session expires.
//
// When an API key is set it takes precedence over session credentials.
type Config struct {
	URL          string `json:"url"`
	APIKey       string `json:"api_key,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	SessionToken string `json:"session_token,omitempty"`
	Project      string `json:"project,omitempty"` // default project slug
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
