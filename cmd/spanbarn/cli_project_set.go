package main

import (
	"flag"
	"fmt"
	"strconv"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// runProjectSet changes per-project settings. Each flag maps to settings keys
// the running server already reads, so no restart is needed (they are cached
// for a short TTL).
func runProjectSet(cfg config.Config, args []string) error {
	const usage = "usage: spanbarn project set <slug> [--trace-ratio=N]"
	if len(args) == 0 {
		return fmt.Errorf(usage)
	}
	slug := args[0]
	fs := flag.NewFlagSet("project set", flag.ContinueOnError)
	traceRatio := fs.Int("trace-ratio", -1, "keep 1 in N traces (1 keeps every trace, 0 returns to the defaults)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *traceRatio < -1 {
		return fmt.Errorf("--trace-ratio must be 0 or more")
	}
	if *traceRatio == -1 {
		return fmt.Errorf("nothing to set. %s", usage)
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	p, err := repo.GetProjectBySlug(slug)
	if err != nil {
		return fmt.Errorf("find project %q: %w", slug, err)
	}
	if err := setTraceRatio(repo, p.ID, *traceRatio); err != nil {
		return err
	}
	if *traceRatio == 0 {
		fmt.Printf("%s: trace ratio back to the defaults\n", slug)
	} else {
		fmt.Printf("%s: keeping 1 in %d traces\n", slug, *traceRatio)
	}
	return nil
}

// setTraceRatio writes both sampling stages. The trace buffer decides which
// traces to keep (ingest.sample_ratio) and the worker then decides which spans
// of an uneventful trace to store (boring.sample_ratio, which defaults to none).
// Setting only the first would keep traces whose spans are then dropped.
// A ratio of 0 removes both overrides.
func setTraceRatio(repo *repository.Repository, projectID int64, ratio int) error {
	keys := []string{
		fmt.Sprintf("ingest.sample_ratio.project.%d", projectID),
		fmt.Sprintf("boring.sample_ratio.project.%d", projectID),
	}
	for _, k := range keys {
		var err error
		if ratio == 0 {
			err = repo.DeleteSetting(k)
		} else {
			err = repo.SetSetting(k, strconv.Itoa(ratio))
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", k, err)
		}
	}
	return nil
}
