package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// openDB opens the SQLite database, runs migrations, and returns a Repository.
func openDB(dbPath string) (*repository.Repository, *sql.DB, error) {
	db, err := repository.NewDB(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}

	if err := repository.Migrate(db.DB); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("run migrations: %w", err)
	}

	repo := repository.NewRepository(db.DB)
	return repo, db.DB, nil
}

// printTable prints a simple aligned table to stdout.
func printTable(headers []string, rows [][]string) {
	if len(headers) == 0 {
		return
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	for i, h := range headers {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Printf("%-*s", widths[i], h)
	}
	fmt.Println()

	for i, w := range widths {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Print(strings.Repeat("-", w))
	}
	fmt.Println()

	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				fmt.Print("  ")
			}
			if i < len(widths) {
				fmt.Printf("%-*s", widths[i], cell)
			}
		}
		fmt.Println()
	}
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

var nonAlphanumDash = regexp.MustCompile(`[^a-z0-9-]+`)

func slugFromName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumDash.ReplaceAllString(s, "")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	return s
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
	tableRows := make([][]string, len(users))
	for i, u := range users {
		tableRows[i] = []string{
			strconv.FormatInt(u.ID, 10),
			u.Username,
			u.CreatedAt.Format(time.RFC3339),
		}
	}
	printTable(headers, tableRows)
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
	tableRows := make([][]string, len(projects))
	for i, p := range projects {
		tableRows[i] = []string{
			strconv.FormatInt(p.ID, 10),
			p.Slug,
			p.Name,
			p.CreatedAt.Format(time.RFC3339),
		}
	}
	printTable(headers, tableRows)
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
	scope := fs.String("scope", "ingest", "Key scope (ingest|read|full)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *name == "" {
		return fmt.Errorf("usage: spanbarn apikey create --project=X --name=Y [--scope=ingest|read|full]")
	}
	if *scope != "ingest" && *scope != "read" && *scope != "full" {
		return fmt.Errorf("scope must be 'ingest', 'read', or 'full'")
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

	projects, err := repo.ListProjects()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	projectSlugs := make(map[int64]string, len(projects))
	for _, p := range projects {
		projectSlugs[p.ID] = p.Slug
	}

	type keyInfo struct {
		id        int64
		projectID int64
		name      string
		scope     string
		lastUsed  string
		created   string
	}
	var keys []keyInfo

	if *project != "" {
		proj, lookupErr := repo.GetProjectBySlug(*project)
		if lookupErr != nil {
			return fmt.Errorf("project %q not found: %w", *project, lookupErr)
		}
		apiKeys, listErr := repo.ListAPIKeys(proj.ID)
		if listErr != nil {
			return fmt.Errorf("list API keys: %w", listErr)
		}
		for _, k := range apiKeys {
			lastUsed := "-"
			if k.LastUsedAt.Valid {
				lastUsed = k.LastUsedAt.Time.Format(time.RFC3339)
			}
			keys = append(keys, keyInfo{k.ID, k.ProjectID, k.Name, k.Scope, lastUsed, k.CreatedAt.Format(time.RFC3339)})
		}
	} else {
		apiKeys, listErr := repo.ListAllAPIKeys()
		if listErr != nil {
			return fmt.Errorf("list API keys: %w", listErr)
		}
		for _, k := range apiKeys {
			lastUsed := "-"
			if k.LastUsedAt.Valid {
				lastUsed = k.LastUsedAt.Time.Format(time.RFC3339)
			}
			keys = append(keys, keyInfo{k.ID, k.ProjectID, k.Name, k.Scope, lastUsed, k.CreatedAt.Format(time.RFC3339)})
		}
	}

	headers := []string{"ID", "Project", "Name", "Scope", "Last Used", "Created"}
	tableRows := make([][]string, len(keys))
	for i, k := range keys {
		projSlug := projectSlugs[k.projectID]
		if projSlug == "" {
			projSlug = strconv.FormatInt(k.projectID, 10)
		}
		tableRows[i] = []string{
			strconv.FormatInt(k.id, 10),
			projSlug,
			k.name,
			k.scope,
			k.lastUsed,
			k.created,
		}
	}
	printTable(headers, tableRows)
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

func runWorkerOnce(_ config.Config) error {
	slog.Info("worker-once: not yet implemented (spool integration pending)")
	return nil
}
