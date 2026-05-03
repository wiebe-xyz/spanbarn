package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
)

// Version and BuildTime are injected at build time via -ldflags.
var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run owns process wiring: it opens storage, starts the worker, and serves the API.
func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg := config.Load()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("spanbarn %s (built %s)\n", Version, BuildTime)
			return nil
		case "worker-once":
			return runWorkerOnce(cfg)
		case "user":
			return runUserCmd(cfg, os.Args[2:])
		case "project":
			return runProjectCmd(cfg, os.Args[2:])
		case "apikey":
			return runAPIKeyCmd(cfg, os.Args[2:])
		}
	}

	if cfg.SessionSecret == "" {
		slog.Warn("SPANBARN_SESSION_SECRET is not set; sessions will not persist across restarts")
	}

	// TODO: Open SQLite storage
	// store, err := storage.Open(cfg.DBPath)
	// if err != nil { return err }
	// defer store.Close()
	slog.Info("storage", "path", cfg.DBPath)

	// TODO: Initialize spool
	// eventSpool, err := spool.NewWithLimit(cfg.SpoolDir, cfg.MaxSpoolBytes)
	// if err != nil { return err }
	// defer eventSpool.Close()
	slog.Info("spool", "dir", cfg.SpoolDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// TODO: Start background worker
	// go runBackgroundWorker(ctx, eventSpool, cfg.SpoolDir, store)

	// TODO: Start HTTP server with real handler
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","version":"%s"}`, Version)
	})

	server := &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// --- User subcommand ---

func runUserCmd(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spanbarn user <create|delete|list>")
	}

	switch args[0] {
	case "create":
		return runUserCreate(cfg, args[1:])
	case "delete":
		return runUserDelete(cfg, args[1:])
	case "list":
		return runUserList(cfg)
	default:
		return fmt.Errorf("unknown user subcommand: %s", args[0])
	}
}

func runUserCreate(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	username := fs.String("username", "", "Username")
	password := fs.String("password", "", "Password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" || *password == "" {
		return fmt.Errorf("usage: spanbarn user create --username=X --password=Y")
	}

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := repo.CreateUser(*username, hash); err != nil {
		return fmt.Errorf("create user: %w", err)
	}

	fmt.Printf("User created: %s\n", *username)
	return nil
}

func runUserDelete(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("user delete", flag.ContinueOnError)
	username := fs.String("username", "", "Username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return fmt.Errorf("usage: spanbarn user delete --username=X")
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := repo.DeleteUser(*username); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}

	fmt.Printf("User deleted: %s\n", *username)
	return nil
}

func runUserList(cfg config.Config) error {
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	users, err := repo.ListUsers()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	headers := []string{"ID", "Username", "Created"}
	rows := make([][]string, len(users))
	for i, u := range users {
		rows[i] = []string{
			strconv.FormatInt(u.ID, 10),
			u.Username,
			u.CreatedAt.Format(time.RFC3339),
		}
	}
	printTable(headers, rows)
	return nil
}

// --- Project subcommand ---

func runProjectCmd(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spanbarn project <create|list>")
	}

	switch args[0] {
	case "create":
		return runProjectCreate(cfg, args[1:])
	case "list":
		return runProjectList(cfg)
	default:
		return fmt.Errorf("unknown project subcommand: %s", args[0])
	}
}

func runProjectCreate(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", "", "Project name")
	slug := fs.String("slug", "", "Project slug (derived from name if omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("usage: spanbarn project create --name=X [--slug=Y]")
	}
	if *slug == "" {
		*slug = slugFromName(*name)
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if _, err := repo.CreateProject(*slug, *name); err != nil {
		return fmt.Errorf("create project: %w", err)
	}

	fmt.Printf("Project created: %s (slug: %s)\n", *name, *slug)
	return nil
}

func runProjectList(cfg config.Config) error {
	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	projects, err := repo.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}

	headers := []string{"ID", "Slug", "Name", "Created"}
	rows := make([][]string, len(projects))
	for i, p := range projects {
		rows[i] = []string{
			strconv.FormatInt(p.ID, 10),
			p.Slug,
			p.Name,
			p.CreatedAt.Format(time.RFC3339),
		}
	}
	printTable(headers, rows)
	return nil
}

// --- API Key subcommand ---

func runAPIKeyCmd(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spanbarn apikey <create|list|revoke>")
	}

	switch args[0] {
	case "create":
		return runAPIKeyCreate(cfg, args[1:])
	case "list":
		return runAPIKeyList(cfg, args[1:])
	case "revoke":
		return runAPIKeyRevoke(cfg, args[1:])
	default:
		return fmt.Errorf("unknown apikey subcommand: %s", args[0])
	}
}

func runAPIKeyCreate(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("apikey create", flag.ContinueOnError)
	project := fs.String("project", "", "Project slug")
	name := fs.String("name", "", "Key name")
	scope := fs.String("scope", "ingest", "Key scope (ingest|full)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *name == "" {
		return fmt.Errorf("usage: spanbarn apikey create --project=X --name=Y [--scope=ingest|full]")
	}
	if *scope != "ingest" && *scope != "full" {
		return fmt.Errorf("scope must be 'ingest' or 'full'")
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	proj, err := repo.GetProjectBySlug(*project)
	if err != nil {
		return fmt.Errorf("project %q not found: %w", *project, err)
	}

	plainKey, err := generateAPIKey()
	if err != nil {
		return err
	}

	keyHash := auth.HashKey(plainKey)

	if _, err := repo.CreateAPIKey(proj.ID, *name, keyHash, *scope); err != nil {
		return fmt.Errorf("create API key: %w", err)
	}

	fmt.Printf("API Key: %s\n", plainKey)
	fmt.Println("API key created. Store this key securely — it won't be shown again.")
	return nil
}

func runAPIKeyList(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("apikey list", flag.ContinueOnError)
	project := fs.String("project", "", "Filter by project slug (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	// Build a map of project ID -> slug for display.
	projects, err := repo.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	projectSlugs := make(map[int64]string, len(projects))
	for _, p := range projects {
		projectSlugs[p.ID] = p.Slug
	}

	var keys []struct {
		id        int64
		projectID int64
		name      string
		scope     string
		lastUsed  string
		created   string
	}

	if *project != "" {
		proj, err := repo.GetProjectBySlug(*project)
		if err != nil {
			return fmt.Errorf("project %q not found: %w", *project, err)
		}
		apiKeys, err := repo.ListAPIKeys(proj.ID)
		if err != nil {
			return fmt.Errorf("list API keys: %w", err)
		}
		for _, k := range apiKeys {
			lastUsed := "-"
			if k.LastUsedAt.Valid {
				lastUsed = k.LastUsedAt.Time.Format(time.RFC3339)
			}
			keys = append(keys, struct {
				id        int64
				projectID int64
				name      string
				scope     string
				lastUsed  string
				created   string
			}{k.ID, k.ProjectID, k.Name, k.Scope, lastUsed, k.CreatedAt.Format(time.RFC3339)})
		}
	} else {
		apiKeys, err := repo.ListAllAPIKeys()
		if err != nil {
			return fmt.Errorf("list API keys: %w", err)
		}
		for _, k := range apiKeys {
			lastUsed := "-"
			if k.LastUsedAt.Valid {
				lastUsed = k.LastUsedAt.Time.Format(time.RFC3339)
			}
			keys = append(keys, struct {
				id        int64
				projectID int64
				name      string
				scope     string
				lastUsed  string
				created   string
			}{k.ID, k.ProjectID, k.Name, k.Scope, lastUsed, k.CreatedAt.Format(time.RFC3339)})
		}
	}

	headers := []string{"ID", "Project", "Name", "Scope", "Last Used", "Created"}
	rows := make([][]string, len(keys))
	for i, k := range keys {
		projSlug := projectSlugs[k.projectID]
		if projSlug == "" {
			projSlug = strconv.FormatInt(k.projectID, 10)
		}
		rows[i] = []string{
			strconv.FormatInt(k.id, 10),
			projSlug,
			k.name,
			k.scope,
			k.lastUsed,
			k.created,
		}
	}
	printTable(headers, rows)
	return nil
}

func runAPIKeyRevoke(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("apikey revoke", flag.ContinueOnError)
	id := fs.Int64("id", 0, "API key ID to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == 0 {
		return fmt.Errorf("usage: spanbarn apikey revoke --id=X")
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := repo.RevokeAPIKey(*id); err != nil {
		return fmt.Errorf("revoke API key: %w", err)
	}

	fmt.Printf("API key revoked: %d\n", *id)
	return nil
}

// --- Worker-once subcommand ---

func runWorkerOnce(cfg config.Config) error {
	slog.Info("worker-once: not yet implemented (spool integration pending)")
	return nil
}
