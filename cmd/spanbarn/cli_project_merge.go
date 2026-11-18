package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/config"
	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/repository/engine"
	"github.com/wiebe-xyz/spanbarn/internal/service"
)

const mergeUsage = "usage: spanbarn project merge --source=SLUG --target=SLUG --env=NAME [--batch=N] [--min-idle=PERCENT]"

// hostIdle samples the host's idle CPU share in percent. A test replaces it.
var hostIdle = readHostIdle

// runProjectMerge moves one project into another (#558): the source's ingest
// keys point at the target and the rows of the core tables follow in batches.
// It prints one line per batch and per table, so a stopped merge leaves a
// record, and a second run moves what is left.
func runProjectMerge(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("project merge", flag.ContinueOnError)
	source := fs.String("source", "", "slug of the project to merge away")
	target := fs.String("target", "", "slug of the canonical project that receives it")
	env := fs.String("env", "", "environment stamped on rows and keys that carry none")
	batch := fs.Int("batch", 500, "rows moved per write")
	minIdle := fs.Float64("min-idle", 35, "refuse to start when the host's idle CPU share is lower (percent, 0 disables)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source == "" || *target == "" || *env == "" {
		return errors.New(mergeUsage)
	}
	if !filter.ValidEnvironmentName(*env) {
		return fmt.Errorf("invalid environment %q: use a lowercase name such as production, staging or testing", *env)
	}
	if *batch <= 0 {
		return errors.New("batch must be positive")
	}
	if err := checkIdleGate(*minIdle); err != nil {
		return err
	}

	repo, db, err := openMergeDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	svc := service.NewProjectService(repo)
	src, err := svc.BySlug(*source)
	if err != nil {
		return fmt.Errorf("find source %q: %w", *source, err)
	}
	tgt, err := svc.BySlug(*target)
	if err != nil {
		return fmt.Errorf("find target %q: %w", *target, err)
	}
	spec := repository.MergeSpec{SourceID: src.ID, TargetID: tgt.ID, SourceSlug: src.Slug, Environment: *env}
	fmt.Printf("merge %s (project %d) into %s (project %d) as environment %s\n", src.Slug, src.ID, tgt.Slug, tgt.ID, *env)

	results, err := svc.Merge(context.Background(), spec, repository.MergeOptions{
		Batch: *batch,
		OnBatch: func(r repository.MergeStepResult) error {
			fmt.Printf("batch %s.%s moved=%d collided=%d\n", r.Family, stepName(r), r.Moved, r.Collided)
			return nil
		},
	})
	printMergeTable(results)
	if err != nil {
		return fmt.Errorf("merge stopped, run it again to resume: %w", err)
	}
	if archived, err := svc.Resolve(src.ID); err == nil && archived == tgt.ID {
		fmt.Printf("%s holds no rows any more and is archived; its old id and slug resolve to %s\n", src.Slug, tgt.Slug)
	}
	return nil
}

// openMergeDB opens the database the way the writer does, shard manager
// included. A merge plans one step per active logs, metrics and prompts shard,
// and without the manager it sees none: it would archive a source that still
// holds rows in the shards, with a "holds no rows any more" message that is
// false. openDB, which the other subcommands use, leaves the shards out.
func openMergeDB(cfg config.Config) (*repository.Repository, *engine.Storage, error) {
	store, err := engine.OpenStorage(context.Background(), cfg.DBPath, engine.StorageOptions{
		CacheMB: engine.DefaultWriterCacheMB, MmapMB: engine.DefaultWriterMmapMB,
		Shards: shardRetention(cfg),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	return repository.ForStorage(store), store, nil
}

// stepName is the table of a step, with the shard it ran in when it has one.
func stepName(r repository.MergeStepResult) string {
	if r.Shard == "" {
		return r.Table
	}
	return r.Table + "@" + r.Shard
}

func printMergeTable(results []repository.MergeStepResult) {
	rows := make([][]string, len(results))
	for i, r := range results {
		rows[i] = []string{r.Family, stepName(r), strconv.FormatInt(r.Moved, 10), strconv.FormatInt(r.Collided, 10)}
	}
	printTable([]string{"Family", "Table", "Moved", "Collided"}, rows)
}

// checkIdleGate refuses a merge while the host is busy (FR-004). A host whose
// idle share cannot be read, such as a developer's machine, is let through with
// a line saying so.
func checkIdleGate(minIdle float64) error {
	if minIdle <= 0 {
		return nil
	}
	idle, err := hostIdle()
	if err != nil {
		fmt.Printf("idle gate skipped: %v\n", err)
		return nil
	}
	if idle < minIdle {
		return fmt.Errorf("host is busy: %.1f%% idle, the gate needs %.1f%%", idle, minIdle)
	}
	fmt.Printf("idle gate passed: %.1f%% idle\n", idle)
	return nil
}

// readHostIdle compares two reads of the aggregate cpu line of /proc/stat taken
// two seconds apart and returns the idle share of the interval in percent.
func readHostIdle() (float64, error) {
	idle1, total1, err := readCPUStat()
	if err != nil {
		return 0, err
	}
	time.Sleep(2 * time.Second)
	idle2, total2, err := readCPUStat()
	if err != nil {
		return 0, err
	}
	if total2 <= total1 {
		return 0, errors.New("cpu counters did not advance")
	}
	return 100 * float64(idle2-idle1) / float64(total2-total1), nil
}

func readCPUStat() (idle, total uint64, err error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, fmt.Errorf("read /proc/stat: %w", err)
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	return parseCPUStat(line)
}

// parseCPUStat reads the "cpu" line: user nice system idle iowait irq softirq
// steal. Idle is the fourth counter and the total is their sum.
func parseCPUStat(line string) (idle, total uint64, err error) {
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, errors.New("unexpected /proc/stat format")
	}
	for i, s := range f[1:] {
		v, perr := strconv.ParseUint(s, 10, 64)
		if perr != nil {
			return 0, 0, fmt.Errorf("parse /proc/stat: %w", perr)
		}
		if i < 8 {
			total += v
		}
		if i == 3 {
			idle = v
		}
	}
	return idle, total, nil
}
