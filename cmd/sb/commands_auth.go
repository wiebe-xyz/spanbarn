package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// --- auth / setup ---

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	urlFlag := fs.String("url", os.Getenv("SPANBARN_URL"), "SpanBarn instance URL")
	apiKey := fs.String("api-key", os.Getenv("SPANBARN_API_KEY"), "read-scoped API key")
	username := fs.String("username", os.Getenv("SPANBARN_USERNAME"), "dashboard username (session login)")
	password := fs.String("password", "", "dashboard password (omit to be prompted)")
	oidc := fs.Bool("oidc", false, "log in via IamBarn device-code flow (browser approval)")
	clientID := fs.String("client-id", os.Getenv("SPANBARN_OIDC_CLIENT_ID"), "IamBarn M2M client id (client_credentials)")
	clientSecret := fs.String("client-secret", os.Getenv("SPANBARN_OIDC_CLIENT_SECRET"), "IamBarn M2M client secret")
	scope := fs.String("scope", "", "OAuth scope for M2M login (optional)")
	project := fs.String("project", "", "default project slug")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *urlFlag == "" {
		return fmt.Errorf("--url is required (or set SPANBARN_URL)")
	}

	cfg := Config{
		URL:     strings.TrimRight(*urlFlag, "/"),
		Project: *project,
	}

	var method string
	switch {
	case *apiKey != "":
		cfg.APIKey = *apiKey
		method = "api-key"
	case *clientID != "" && *clientSecret != "":
		if err := clientCredentialsLogin(&cfg, "", *clientID, *clientSecret, *scope); err != nil {
			return err
		}
		method = "oidc-m2m"
	case *oidc:
		if err := deviceLogin(&cfg); err != nil {
			return err
		}
		method = "oidc-device"
	case *username != "":
		if *password == "" {
			pw, err := promptPassword()
			if err != nil {
				return err
			}
			*password = pw
		}
		token, err := loginWithPassword(cfg.URL, *username, *password)
		if err != nil {
			return err
		}
		cfg.Username = *username
		cfg.Password = *password
		cfg.SessionToken = token
		method = "session"
	default:
		return fmt.Errorf("provide one of: --api-key, --oidc (IamBarn device login), --client-id/--client-secret (M2M), or --username")
	}

	// Verify auth works against a read endpoint.
	client := &Client{base: cfg.URL, http: &http.Client{Timeout: 10 * time.Second}, cfg: cfg}
	if _, err := client.get("/api/v1/projects"); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if err := saveConfig(client.cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s (%s)\n", cfg.URL, method)
	writeOut(map[string]any{"ok": true, "url": cfg.URL, "auth": method})
	return nil
}

// loginWithPassword posts credentials to /api/v1/login and returns the session
// token from the Set-Cookie response.
func loginWithPassword(baseURL, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := http.Post(baseURL+"/api/v1/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed: HTTP %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "session" {
			return ck.Value, nil
		}
	}
	return "", fmt.Errorf("no session cookie in login response")
}

// promptPassword reads a password from the terminal without echoing.
func promptPassword() (string, error) {
	fmt.Fprint(os.Stderr, "Password: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(pw), nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	projectFlag := fs.String("project", "", "project slug (skip interactive picker)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, err := newClient()
	if err != nil {
		return err
	}

	slug := *projectFlag
	if slug == "" {
		picked, err := pickProject(client)
		if err != nil {
			return err
		}
		slug = picked
	} else if err := validateProject(client, slug); err != nil {
		return err
	}

	if err := saveLocalConfig(LocalConfig{Project: slug}); err != nil {
		return fmt.Errorf("write %s: %w", localConfigFile, err)
	}
	fmt.Fprintf(os.Stderr, "Project set to %q — saved to %s\n", slug, localConfigFile)
	writeOut(map[string]any{"ok": true, "project": slug, "file": localConfigFile})
	return nil
}
