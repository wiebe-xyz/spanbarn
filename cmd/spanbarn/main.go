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

	"github.com/wiebe-xyz/spanbarn/internal/aggregation"
	"github.com/wiebe-xyz/spanbarn/internal/alert"
	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/observability"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/retention"
	"github.com/wiebe-xyz/spanbarn/internal/service"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
	"github.com/wiebe-xyz/spanbarn/internal/worker"
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
	logger, shutdownObservability := observability.Setup(Version)
	slog.SetDefault(logger)
	defer shutdownObservability()

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

	// 1. Open SQLite database and run migrations.
	db, err := repository.NewDB(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := repository.Migrate(db.DB); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	logger.Info("storage", "path", cfg.DBPath)

	// 2. Create repository.
	repo := repository.NewRepository(db.DB)

	// 3. Create spool.
	eventSpool, err := spool.NewSpool(cfg.SpoolDir, cfg.MaxSpoolBytes)
	if err != nil {
		return fmt.Errorf("create spool: %w", err)
	}
	defer eventSpool.Close()
	logger.Info("spool", "dir", cfg.SpoolDir)

	// 4. Create ingest queue and handler.
	queue := ingest.NewQueue(32768)
	ingestHandler := ingest.NewHandler(queue, eventSpool, 5*time.Millisecond, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 5. Start ingest handler background goroutine.
	ingestHandler.Start(ctx)

	// 6. Create and start the spool-to-DB worker.
	w := worker.NewWorker(eventSpool, &workerRepoAdapter{repo: repo}, logger)
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()
	go w.Run(workerCtx)

	// 7. Create aggregator and retention worker.
	aggInterval := parseAggregationInterval(cfg.AggregationInterval)
	aggregator := aggregation.NewAggregator(repo, aggInterval, logger)

	retentionCfg := retention.Config{
		FullRetentionHours:     cfg.RetentionFullHours,
		ErrorRetentionDays:     cfg.RetentionErrorDays,
		AggregateRetentionDays: cfg.RetentionAggregatedDays,
		SlowThresholdUS:        int64(cfg.SlowThresholdMS) * 1000,
	}
	retentionWorker := retention.NewRetentionWorker(repo, aggregator, retentionCfg, logger)
	retentionCtx, retentionCancel := context.WithCancel(ctx)
	defer retentionCancel()
	go retentionWorker.Run(retentionCtx)

	// 7b. Create and start alert runner.
	alertNotifier := alert.NewDefaultNotifier(alert.NotifierConfig{}, logger)
	alertEval := alert.NewEvaluator(repo, alertNotifier, logger)
	alertRunner := alert.NewRunner(alertEval, repo, time.Minute, logger)
	alertCtx, alertCancel := context.WithCancel(ctx)
	defer alertCancel()
	go alertRunner.Run(alertCtx)

	// 8. Create auth components.
	authorizer := auth.NewAuthorizer(cfg.APIKeySHA256, &keyLookupAdapter{repo: repo}, logger)
	_ = authorizer // used indirectly via cfg.APIKey in the server
	userAuth := auth.NewUserAuthenticator(&userLookupAdapter{repo: repo}, logger)
	sessionMgr := auth.NewSessionManager(cfg.SessionSecret, int64(cfg.SessionTTLSeconds))

	// 9. Create query service.
	querySvc := service.NewQueryService(repo, logger)

	// 10. Create API server.
	serverCfg := api.ServerConfig{
		APIKey:         cfg.APIKey,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		AllowedOrigins: cfg.AllowedOrigins,
		Version:        Version,
		MetricsToken:   cfg.MetricsToken,
		LoginRate:      cfg.LoginRatePerMinute,
		IngestRate:     cfg.IngestRatePerMinute,
		APIRate:        cfg.APIRatePerMinute,
	}
	apiServer := api.NewServerWithQuery(serverCfg, ingestHandler, querySvc, sessionMgr, logger, api.WithRepository(repo))

	// 11. Build the final HTTP handler, adding login/logout routes.
	mux := http.NewServeMux()
	loginRL := api.RateLimitMiddleware(api.NewRateLimiter(cfg.LoginRatePerMinute, cfg.IngestRatePerMinute, cfg.APIRatePerMinute), "login")
	mux.Handle("/api/v1/login", loginRL(api.HandleLogin(userAuth, sessionMgr)))
	mux.Handle("/api/v1/logout", http.HandlerFunc(api.HandleLogout()))
	mux.Handle("/", apiServer.Handler())

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	// 12. Wait for shutdown signal or server error.
	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	// 13. Graceful shutdown sequence.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown error", "error", err)
	}

	alertCancel()
	retentionCancel()
	ingestHandler.Stop()
	workerCancel()
	// eventSpool.Close() and db.Close() handled by defers.

	logger.Info("shutdown complete")
	return nil
}

// parseAggregationInterval parses a duration string with a fallback of 1 minute.
func parseAggregationInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Minute
	}
	return d
}

// --- Adapters to bridge repository types to package-specific interfaces ---

// workerRepoAdapter adapts *repository.Repository to worker.Repository.
type workerRepoAdapter struct {
	repo *repository.Repository
}

func (a *workerRepoAdapter) InsertSpans(_ context.Context, spans []repository.Span) error {
	return a.repo.InsertSpans(spans)
}

// keyLookupAdapter adapts *repository.Repository to auth.KeyLookup.
type keyLookupAdapter struct {
	repo *repository.Repository
}

func (a *keyLookupAdapter) GetAPIKeyByHash(keyHash string) (auth.APIKeyRecord, error) {
	k, err := a.repo.GetAPIKeyByHash(keyHash)
	if err != nil {
		return auth.APIKeyRecord{}, err
	}
	return auth.APIKeyRecord{
		ID:        k.ID,
		ProjectID: k.ProjectID,
		Scope:     k.Scope,
	}, nil
}

func (a *keyLookupAdapter) TouchAPIKey(id int64) error {
	return a.repo.TouchAPIKey(id)
}

// userLookupAdapter adapts *repository.Repository to auth.UserLookup.
type userLookupAdapter struct {
	repo *repository.Repository
}

func (a *userLookupAdapter) GetUserByUsername(username string) (auth.UserRecord, error) {
	u, err := a.repo.GetUserByUsername(username)
	if err != nil {
		return auth.UserRecord{}, err
	}
	return auth.UserRecord{
		ID:           u.ID,
		Username:     u.Username,
		PasswordHash: u.PasswordHash,
	}, nil
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
