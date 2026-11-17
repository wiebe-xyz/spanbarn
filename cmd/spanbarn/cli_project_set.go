package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// runProjectSet changes per-project settings. Each flag maps to settings keys
// the running server already reads, so no restart is needed (they are cached
// for a short TTL).
func runProjectSet(cfg config.Config, args []string) error {
	const usage = "usage: spanbarn project set <slug> [--trace-ratio=N [--op=NAME]] [--log-retention-hours=H]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	slug := args[0]
	fs := flag.NewFlagSet("project set", flag.ContinueOnError)
	traceRatio := fs.Int("trace-ratio", -1, "keep 1 in N traces (1 keeps every trace, 0 returns to the defaults)")
	op := fs.String("op", "", "with --trace-ratio: apply the ratio to traces whose root operation is NAME only")
	logHours := fs.Int("log-retention-hours", -1, "delete the project's logs older than H hours, error-trace logs included (0 removes the cap)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *traceRatio < -1 {
		return fmt.Errorf("--trace-ratio must be 0 or more")
	}
	if *logHours < -1 {
		return fmt.Errorf("--log-retention-hours must be 0 or more")
	}
	if *op != "" && *traceRatio == -1 {
		return fmt.Errorf("--op needs --trace-ratio. %s", usage)
	}
	if *traceRatio == -1 && *logHours == -1 {
		return fmt.Errorf("nothing to set. %s", usage)
	}

	repo, db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	p, err := service.NewProjectService(repo).BySlug(slug)
	if err != nil {
		return fmt.Errorf("find project %q: %w", slug, err)
	}
	if *traceRatio >= 0 {
		if err := applyTraceRatio(service.NewSettingsService(repo), slug, p.ID, *op, *traceRatio); err != nil {
			return err
		}
	}
	if *logHours >= 0 {
		if err := setLogRetention(service.NewSettingsService(repo), p.ID, *logHours); err != nil {
			return err
		}
		if *logHours == 0 {
			fmt.Printf("%s: log retention back to the global window\n", slug)
		} else {
			fmt.Printf("%s: deleting logs older than %d hours\n", slug, *logHours)
		}
	}
	return nil
}

// setLogRetention caps one project's log retention. The retention worker reads
// the key each cycle; 0 removes the cap.
func setLogRetention(settings *service.SettingsService, projectID int64, hours int) error {
	key := fmt.Sprintf("retention.log_hours.project.%d", projectID)
	if hours == 0 {
		return settings.Delete(key)
	}
	return settings.Set(key, strconv.Itoa(hours))
}

// setTraceRatio writes both sampling stages. The trace buffer decides which
// traces to keep (ingest.sample_ratio) and the worker then decides which spans
// of an uneventful trace to store (boring.sample_ratio, which defaults to none).
// Setting only the first would keep traces whose spans are then dropped.
// A ratio of 0 removes both overrides.
func setTraceRatio(settings *service.SettingsService, projectID int64, ratio int) error {
	keys := []string{
		fmt.Sprintf("ingest.sample_ratio.project.%d", projectID),
		fmt.Sprintf("boring.sample_ratio.project.%d", projectID),
	}
	for _, k := range keys {
		var err error
		if ratio == 0 {
			err = settings.Delete(k)
		} else {
			err = settings.Set(k, strconv.Itoa(ratio))
		}
		if err != nil {
			return fmt.Errorf("write %s: %w", k, err)
		}
	}
	return nil
}

// setOperationTraceRatio keeps 1 in ratio of the traces whose root operation is
// op, ahead of the project's own ratio (ratio 1 keeps every one, 0 removes the
// override). Only the trace buffer stage has a per-operation key. The worker's
// boring stage still keeps one trace per project and operation per minute, so
// a rare operation such as a boot check-in survives it; two of them inside one
// minute keep the first. Set --trace-ratio without --op to keep every span.
func setOperationTraceRatio(settings *service.SettingsService, slug string, projectID int64, op string, ratio int) error {
	key := fmt.Sprintf("ingest.sample_ratio.project.%d.op.%s", projectID, op)
	if ratio == 0 {
		if err := settings.Delete(key); err != nil {
			return fmt.Errorf("write %s: %w", key, err)
		}
		fmt.Printf("%s: trace ratio for %s back to the project's\n", slug, op)
		return nil
	}
	if err := settings.Set(key, strconv.Itoa(ratio)); err != nil {
		return fmt.Errorf("write %s: %w", key, err)
	}
	fmt.Printf("%s: keeping 1 in %d traces rooted at %s\n", slug, ratio, op)
	return nil
}

// applyTraceRatio writes the project's ratio, or one operation's when op is set.
func applyTraceRatio(settings *service.SettingsService, slug string, projectID int64, op string, ratio int) error {
	if op != "" {
		return setOperationTraceRatio(settings, slug, projectID, op, ratio)
	}
	if err := setTraceRatio(settings, projectID, ratio); err != nil {
		return err
	}
	if ratio == 0 {
		fmt.Printf("%s: trace ratio back to the defaults\n", slug)
	} else {
		fmt.Printf("%s: keeping 1 in %d traces\n", slug, ratio)
	}
	return nil
}
