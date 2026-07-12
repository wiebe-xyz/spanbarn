package main

import (
	"context"
	"flag"
	"fmt"
	"sort"
	"strconv"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// --- DB subcommand ---

func runDBCmd(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spanbarn db <snapshot-settings>")
	}

	switch args[0] {
	case "snapshot-settings":
		return runDBSnapshotSettings(cfg, args[1:])
	default:
		return fmt.Errorf("unknown db subcommand: %s", args[0])
	}
}

// runDBSnapshotSettings builds a settings-only snapshot database — projects,
// users, API keys, alert rules, org settings, saved queries, and trace
// exclusions, with every telemetry table present but empty — for disaster
// recovery. The output file is a complete, ready-to-serve spanbarn.db: drop
// it in as SPANBARN_DB_PATH and start the binary, no further restore step
// needed.
func runDBSnapshotSettings(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("db snapshot-settings", flag.ContinueOnError)
	out := fs.String("out", "", "Output path for the settings-only snapshot database")
	src := fs.String("src", "", "Source database path (defaults to SPANBARN_DB_PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("usage: spanbarn db snapshot-settings --out=PATH [--src=PATH]")
	}
	srcPath := *src
	if srcPath == "" {
		srcPath = cfg.DBPath
	}

	counts, err := repository.SnapshotSettings(context.Background(), srcPath, *out)
	if err != nil {
		return fmt.Errorf("snapshot settings: %w", err)
	}

	fmt.Printf("Settings snapshot written to %s\n", *out)
	tables := make([]string, 0, len(counts))
	for t := range counts {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	headers := []string{"Table", "Rows"}
	rows := make([][]string, len(tables))
	for i, t := range tables {
		rows[i] = []string{t, strconv.FormatInt(counts[t], 10)}
	}
	printTable(headers, rows)
	return nil
}
