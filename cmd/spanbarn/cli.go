package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

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

	// Calculate column widths.
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

	// Print header.
	for i, h := range headers {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Printf("%-*s", widths[i], h)
	}
	fmt.Println()

	// Print separator.
	for i, w := range widths {
		if i > 0 {
			fmt.Print("  ")
		}
		fmt.Print(strings.Repeat("-", w))
	}
	fmt.Println()

	// Print rows.
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

// generateAPIKey generates a cryptographically random API key (32 bytes, hex-encoded = 64 chars).
func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// slugFromName derives a URL-safe slug from a human-readable name.
var nonAlphanumDash = regexp.MustCompile(`[^a-z0-9-]+`)

func slugFromName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumDash.ReplaceAllString(s, "")
	// Collapse multiple dashes.
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	return s
}
