// Command shardspike measures the storage layouts proposed in issue #235 on a
// synthetic, production-shaped dataset:
//
//	L1 single  — today's single SQLite file
//	L2 split   — spans + trace_summaries in spans.db, logs/metrics/prompts/
//	             error samples in heavy.db, config tables in config.db
//	L3 shards  — one file per UTC day holding the in-scope tables, plus config.db
//
// Usage:
//
//	shardspike gen     -dir DIR [-scale F] [-spans N]   build all three layouts
//	shardspike counts  -dir DIR                          row counts per layout
//	shardspike reads      -dir DIR [-runs N]             idle read latency per layout
//	shardspike contention -dir DIR [-duration D]         span reads under prod-rate writes, L1 vs L2
//	shardspike retention  -dir DIR [-duration D]         expire one day: DELETE vs shard file delete
//	shardspike all        -dir DIR                       reads, contention, retention in that order
//
// It is a measuring tool for a spike and is not part of the server. It never
// opens a production database: every file it touches lives under -dir.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

// spikeSeed fixes the dataset, so repeated runs measure the same rows.
const spikeSeed = 235

type options struct {
	dir   string
	scale float64
	spans int
	days  int
	end   time.Time
	// runs is how many times each read query is timed.
	runs int
	// duration bounds every timed write-load run (contention, retention).
	duration time.Duration
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "shardspike:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: shardspike <gen|counts|reads|contention|retention|all> -dir DIR [flags]")
	}
	opts, err := parseOptions(args[0], args[1:])
	if err != nil {
		return err
	}
	switch args[0] {
	case "gen":
		return cmdGen(ctx, opts, out)
	case "counts":
		return cmdCounts(ctx, opts, out)
	case "reads":
		return cmdReads(ctx, opts, out)
	case "contention":
		return cmdContention(ctx, opts, out)
	case "retention":
		return cmdRetention(ctx, opts, out)
	case "all":
		return cmdAll(ctx, opts, out)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func parseOptions(name string, args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&o.dir, "dir", "", "directory for the generated layouts (required)")
	fs.Float64Var(&o.scale, "scale", 1, "row-count multiplier on the prod profile")
	fs.IntVar(&o.spans, "spans", 0, "override the span count of the prod profile")
	fs.IntVar(&o.days, "days", 7, "days of data, one shard file per day")
	fs.IntVar(&o.runs, "runs", 20, "timed runs per read query")
	fs.DurationVar(&o.duration, "duration", 30*time.Second, "length of each write-load run")
	end := fs.String("end", "2026-10-04", "UTC day the dataset ends at (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.dir == "" {
		return o, fmt.Errorf("-dir is required")
	}
	t, err := time.Parse(time.DateOnly, *end)
	if err != nil {
		return o, fmt.Errorf("-end: %w", err)
	}
	o.end = t
	return o, nil
}

func (o options) profile() profile {
	p := prodProfile()
	p.Days = o.days
	if o.spans > 0 {
		p.Spans = o.spans
	}
	return p.scaled(o.scale)
}

func (o options) window() window { return newWindow(o.end, o.days) }

func cmdGen(ctx context.Context, o options, out io.Writer) error {
	p := o.profile()
	fmt.Fprintf(out, "profile: %s\n", p)
	if _, err := os.Stat(o.dir); err == nil {
		return fmt.Errorf("%s already exists; pick a fresh -dir", o.dir)
	}
	steps := []struct {
		name string
		fn   func() error
	}{
		{layoutSingle, func() error { return buildSingle(ctx, o.dir, newGenerator(p, o.window(), spikeSeed)) }},
		{layoutSplit, func() error { return buildFromSingle(ctx, o.dir, layoutSplit, splitPlans()) }},
		{layoutShards, func() error { return buildFromSingle(ctx, o.dir, layoutShards, shardPlans(o.window())) }},
	}
	for _, s := range steps {
		start := time.Now()
		if err := s.fn(); err != nil {
			return fmt.Errorf("build %s: %w", s.name, err)
		}
		fmt.Fprintf(out, "built %-7s in %s\n", s.name, time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// cmdAll runs the measurements in an order where none disturbs the next:
// reads first on the untouched data, contention (which inserts a few thousand
// rows), then retention (which works on copies).
func cmdAll(ctx context.Context, o options, out io.Writer) error {
	for _, fn := range []func(context.Context, options, io.Writer) error{cmdCounts, cmdReads, cmdContention, cmdRetention} {
		if err := fn(ctx, o, out); err != nil {
			return err
		}
	}
	return nil
}

func cmdCounts(ctx context.Context, o options, out io.Writer) error {
	for _, l := range layouts {
		counts, err := rowCounts(ctx, o.dir, l)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%-7s", l)
		for _, t := range inScope {
			fmt.Fprintf(out, " %s=%d", t, counts[t])
		}
		fmt.Fprintln(out)
	}
	return nil
}

// rowCounts counts every in-scope table through the layout's reader.
func rowCounts(ctx context.Context, root, layout string) (map[string]int64, error) {
	db, err := openReader(ctx, root, layout)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	counts := map[string]int64{}
	for _, t := range inScope {
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("%s count %s: %w", layout, t, err)
		}
		counts[t] = n
	}
	return counts, nil
}
