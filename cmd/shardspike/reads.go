package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// readProject is the project that holds ~86% of the spans.
const readProject = 1

// timeRange is a closed ingested_at range a query reads.
type timeRange struct{ from, to time.Time }

// readCase is one timed repository call. run returns a result size (rows, or
// spans scanned) that must be the same in every layout.
type readCase struct {
	label string
	run   func(ctx context.Context, r *repository.Repository) (int, error)
}

// readIDs are the ids the by-id queries use. They are picked once from L1 so
// every layout runs the same call.
type readIDs struct{ trace, cross, logTrace, metric string }

// cellResult is what one query measured on one layout.
type cellResult struct {
	cold time.Duration
	warm timings
	size int
	err  error
}

// cmdReads times the read queries of issue #235 on L1, L2 and L3 and prints
// result sizes, query plans and the cost of attaching shards.
func cmdReads(ctx context.Context, o options, out io.Writer) error {
	ids, err := pickIDs(ctx, o.dir)
	if err != nil {
		return err
	}
	cases, notes := readCases(o.window(), ids)
	results := map[string][]cellResult{}
	for _, l := range layouts {
		res, err := runLayout(ctx, o, l, cases)
		if err != nil {
			return err
		}
		results[l] = res
	}
	fmt.Fprintln(out, "## Reads (idle)")
	fmt.Fprintf(out, "\nProfile: %s, runs=%d (warm: after one untimed warm-up). Cold: first call on a freshly opened reader, OS page cache not dropped.\n\n", o.profile(), o.runs)
	if err := printSizes(out, o.dir); err != nil {
		return err
	}
	printReadTable(out, cases, results)
	printReadNotes(out, cases, results, notes)
	if err := printPlans(ctx, out, o, ids); err != nil {
		return err
	}
	return printAttachCost(ctx, out, o)
}

// runLayout opens the layout's reader and measures every case on it.
func runLayout(ctx context.Context, o options, layout string, cases []readCase) ([]cellResult, error) {
	db, err := openReader(ctx, o.dir, layout)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	repo := repository.NewReadOnlyRepository(db)
	repo.SetQueryTimeout(2 * time.Minute)
	res := make([]cellResult, len(cases))
	for i, c := range cases {
		start := time.Now()
		res[i].size, res[i].err = c.run(ctx, repo)
		res[i].cold = time.Since(start)
	}
	for i, c := range cases {
		if res[i].err != nil {
			continue
		}
		run := func() error { _, err := c.run(ctx, repo); return err }
		if res[i].err = run(); res[i].err == nil {
			res[i].warm, res[i].err = measure(o.runs, run)
		}
	}
	return res, nil
}

// pickIDs picks the by-id inputs from L1, deterministically.
func pickIDs(ctx context.Context, root string) (readIDs, error) {
	db, err := openReader(ctx, root, layoutSingle)
	if err != nil {
		return readIDs{}, err
	}
	defer db.Close()
	pick := func(q string) string {
		v, err := queryStrings(ctx, db, q)
		if err != nil || len(v) == 0 {
			return ""
		}
		return v[0]
	}
	var ids readIDs
	ids.trace = pick("SELECT trace_id FROM trace_summaries ORDER BY trace_id LIMIT 1 OFFSET 7")
	if ids.trace == "" {
		ids.trace = pick("SELECT trace_id FROM trace_summaries ORDER BY trace_id LIMIT 1")
	}
	ids.cross = pick("SELECT trace_id FROM spans GROUP BY trace_id " +
		"HAVING substr(min(ingested_at),1,10) <> substr(max(ingested_at),1,10) ORDER BY trace_id LIMIT 1")
	ids.logTrace = pick("SELECT trace_id FROM logs WHERE project_id = 1 ORDER BY trace_id LIMIT 1")
	ids.metric = pick("SELECT name FROM metrics WHERE project_id = 1 GROUP BY name ORDER BY COUNT(*) DESC, name LIMIT 1")
	return ids, nil
}

// rangeNames label the three time ranges of rangeCases.
var rangeNames = []string{"1 shard", "2 shards", "full window"}

// rangeCases are the time ranges that touch 1 shard, 2 shards and the whole
// window.
func rangeCases(w window) []timeRange {
	days := w.days()
	d0, d1 := days[min(1, len(days)-1)], days[min(2, len(days)-1)]
	return []timeRange{
		{d0.Add(6 * time.Hour), d0.Add(18 * time.Hour)},
		{d0.Add(12 * time.Hour), d1.Add(12 * time.Hour)},
		{w.Start, w.End},
	}
}

// readCases builds the query list. notes explains cases that were skipped.
func readCases(w window, ids readIDs) ([]readCase, []string) {
	last := timeRange{w.End.Add(-24 * time.Hour), w.End}
	full := timeRange{w.Start, w.End}
	var notes []string
	cases := []readCase{traceListCase("trace list, 24h", last)}
	cases = append(cases, byIDCases(ids, &notes)...)
	cases = append(cases,
		analyzeCase("analyze group-by service, 24h", last),
		analyzeCase("analyze group-by service, full window", full),
		attrCase("attribute compare, 24h", last),
		heatmapCase("heatmap, 24h", last))
	cases = append(cases, logMetricCases(ids, last, full, &notes)...)
	cases = append(cases, promptsCase("prompts list, newest 50", full))
	for i, r := range rangeCases(w) {
		cases = append(cases,
			traceListCase("trace list, "+rangeNames[i], r),
			analyzeCase("analyze group-by service, "+rangeNames[i], r))
	}
	return cases, notes
}

// optional appends c when ok, and otherwise records why it was skipped.
func optional(out []readCase, ok bool, notes *[]string, skip string, c readCase) []readCase {
	if !ok {
		*notes = append(*notes, skip)
		return out
	}
	return append(out, c)
}

func byIDCases(ids readIDs, notes *[]string) []readCase {
	out := optional(nil, ids.trace != "", notes, "trace detail skipped: no trace in trace_summaries",
		traceDetailCase("trace detail", ids.trace))
	return optional(out, ids.cross != "", notes, "trace detail across midnight skipped: no trace spans two UTC days",
		traceDetailCase("trace detail, crosses UTC midnight", ids.cross))
}

func logMetricCases(ids readIDs, last, full timeRange, notes *[]string) []readCase {
	out := optional(nil, ids.logTrace != "", notes, "logs by trace skipped: no log with a trace id for project 1",
		logsByTraceCase("logs by trace", ids.logTrace, full))
	out = append(out, logHistCase("log histogram, 24h", last))
	return optional(out, ids.metric != "", notes, "metric series skipped: no metric for project 1",
		metricCase("metric series, 24h", ids.metric, last))
}

func traceListCase(label string, r timeRange) readCase {
	return readCase{label, func(_ context.Context, repo *repository.Repository) (int, error) {
		rows, err := repo.SearchTraceSummaries(repository.SpanFilter{ProjectID: readProject, From: r.from, To: r.to, Limit: 50}, 0)
		return len(rows), err
	}}
}

func traceDetailCase(label, traceID string) readCase {
	return readCase{label, func(_ context.Context, repo *repository.Repository) (int, error) {
		spans, err := repo.GetTraceByID(traceID)
		return len(spans), err
	}}
}

func analyzeCase(label string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		res, err := repo.Analyze(ctx, repository.AnalyzeQuery{
			ProjectID: readProject, From: r.from, To: r.to, GroupBy: []string{"service"},
			Calcs:    []repository.AnalyzeCalc{{Fn: repository.CalcCount}, {Fn: repository.CalcP95}},
			Desc:     true,
			Limit:    20,
			Sample:   1,
			MaxSpans: 5_000_000,
		})
		if err != nil {
			return 0, err
		}
		return int(res.Scanned), nil
	}}
}

func attrCase(label string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		res, err := repo.ScanAttributeSet(ctx, repository.AttributeSetWindow{
			ProjectID: readProject, From: r.from, To: r.to, Sample: 1, MaxSpans: 200_000, ValueCap: 20})
		if err != nil {
			return 0, err
		}
		return int(res.Scanned), nil
	}}
}

func heatmapCase(label string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		res, err := repo.ScanHeatmap(ctx, repository.HeatmapWindow{
			ProjectID: readProject, From: r.from, To: r.to, MaxSpans: 200_000, TimeBuckets: 24, DurationBuckets: 20})
		if err != nil {
			return 0, err
		}
		return int(res.Scanned), nil
	}}
}

func logsByTraceCase(label, traceID string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		_, total, err := repo.QueryLogs(ctx, repository.LogFilter{
			ProjectID: readProject, TraceID: traceID, From: r.from, To: r.to, Limit: 100})
		return total, err
	}}
}

func logHistCase(label string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		b, err := repo.LogHistogram(ctx, repository.LogFilter{ProjectID: readProject, From: r.from, To: r.to}, 3600)
		return len(b), err
	}}
}

func metricCase(label, name string, r timeRange) readCase {
	return readCase{label, func(ctx context.Context, repo *repository.Repository) (int, error) {
		rows, err := repo.QueryMetricSeries(ctx, repository.MetricFilter{
			ProjectID: readProject, Name: name, From: r.from, To: r.to, Limit: 10000})
		return len(rows), err
	}}
}

func promptsCase(label string, r timeRange) readCase {
	return readCase{label, func(_ context.Context, repo *repository.Repository) (int, error) {
		rows, err := repo.QueryPromptRecords(repository.PromptFilter{ProjectID: readProject, From: r.from, To: r.to, Limit: 50})
		return len(rows), err
	}}
}

// layoutTitle is the name the output uses for a layout.
func layoutTitle(layout string) string {
	return map[string]string{layoutSingle: "L1 single", layoutSplit: "L2 split", layoutShards: "L3 shards"}[layout]
}

// dirBytes counts the *.db files under dir and sums them with their *.db-wal.
func dirBytes(dir string) (files int, size int64, err error) {
	for _, pat := range []string{"*.db", "*.db-wal"} {
		matches, err := filepath.Glob(filepath.Join(dir, pat))
		if err != nil {
			return 0, 0, err
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				return 0, 0, err
			}
			size += info.Size()
			if pat == "*.db" {
				files++
			}
		}
	}
	return files, size, nil
}

func printSizes(out io.Writer, root string) error {
	fmt.Fprintln(out, "| layout | files | size on disk |\n|---|---|---|")
	for _, l := range layouts {
		n, size, err := dirBytes(layoutDir(root, l))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "| %s | %d | %.2f MB |\n", layoutTitle(l), n, float64(size)/(1<<20))
	}
	fmt.Fprintln(out)
	return nil
}

func printReadTable(out io.Writer, cases []readCase, results map[string][]cellResult) {
	fmt.Fprintln(out, "| query | L1 p50 | L1 p95 | L2 p50 | L2 p95 | L3 p50 | L3 p95 | L1 cold | L2 cold | L3 cold | rows match |")
	fmt.Fprintln(out, "|"+strings.Repeat("---|", 11))
	for i, c := range cases {
		warm, cold := []string{c.label}, []string{}
		for _, l := range layouts {
			r := results[l][i]
			if r.err != nil {
				warm, cold = append(warm, "error", "error"), append(cold, "error")
				continue
			}
			warm, cold = append(warm, ms(r.warm.pct(0.50)), ms(r.warm.pct(0.95))), append(cold, ms(r.cold))
		}
		fmt.Fprintf(out, "| %s | %s | %s |\n", strings.Join(warm, " | "), strings.Join(cold, " | "), rowsMatch(results, i))
	}
	fmt.Fprintln(out)
}

// rowsMatch compares case i's result size on L2 and L3 against L1.
func rowsMatch(results map[string][]cellResult, i int) string {
	base := results[layoutSingle][i]
	if base.err != nil {
		return "n/a"
	}
	var bad []string
	for _, l := range []string{layoutSplit, layoutShards} {
		r := results[l][i]
		if r.err == nil && r.size != base.size {
			bad = append(bad, fmt.Sprintf("%s=%d", layoutTitle(l)[:2], r.size))
		}
	}
	if len(bad) > 0 {
		return fmt.Sprintf("MISMATCH (L1=%d, %s)", base.size, strings.Join(bad, ", "))
	}
	return fmt.Sprintf("yes (%d)", base.size)
}

// printReadNotes lists skipped cases and queries that returned an error.
func printReadNotes(out io.Writer, cases []readCase, results map[string][]cellResult, notes []string) {
	for _, l := range layouts {
		for i, c := range cases {
			if err := results[l][i].err; err != nil {
				notes = append(notes, fmt.Sprintf("%s, %s failed: %v", layoutTitle(l), c.label, err))
			}
		}
	}
	for _, n := range notes {
		fmt.Fprintf(out, "- %s\n", n)
	}
	fmt.Fprintln(out)
}

// planStatements mirror the SQL of the repository queries, with the same
// predicates, so their plans show how each layout resolves them.
var planStatements = []struct{ name, sql string }{
	{"trace list (trace_summaries)", "SELECT trace_id FROM trace_summaries WHERE project_id = :p AND ingested_at >= :from AND ingested_at <= :to ORDER BY ingested_at DESC LIMIT 50"},
	{"spans by trace_id", "SELECT id FROM spans WHERE trace_id = :trace ORDER BY start_time_us"},
	{"spans range", "SELECT id, duration_us FROM spans WHERE project_id = :p AND ingested_at >= :from AND ingested_at <= :to"},
	{"logs by trace_id", "SELECT id FROM logs WHERE project_id = :p AND ingested_at >= :from AND ingested_at <= :to AND trace_id = :logtrace"},
	{"metrics series", "SELECT id FROM metrics WHERE project_id = :p AND name = :metric AND ingested_at >= :from AND ingested_at <= :to ORDER BY time_unix_nano"},
}

// planArgs binds every named parameter the plan statements use.
func planArgs(w window, ids readIDs) []any {
	return []any{sql.Named("p", readProject), sql.Named("from", sqlTime(w.End.Add(-24*time.Hour))),
		sql.Named("to", sqlTime(w.End)), sql.Named("trace", ids.trace),
		sql.Named("logtrace", ids.logTrace), sql.Named("metric", ids.metric)}
}

// printPlans prints EXPLAIN QUERY PLAN of every statement on every layout, with
// a count of index SEARCH steps against full SCAN steps.
func printPlans(ctx context.Context, out io.Writer, o options, ids readIDs) error {
	fmt.Fprintln(out, "### Query plans")
	fmt.Fprintln(out, "\nSEARCH steps use an index lookup, SCAN steps read a whole table or index. In L3 each shard appears as its own step, so USING INDEX per shard shows the shard indexes are used through the view. A CO-ROUTINE followed by SCAN means the view is materialised before ORDER BY and LIMIT apply.")
	for _, st := range planStatements {
		fmt.Fprintf(out, "\n**%s**\n", st.name)
		for _, l := range layouts {
			if err := printPlan(ctx, out, o, l, st.sql, planArgs(o.window(), ids)); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(out)
	return nil
}

func printPlan(ctx context.Context, out io.Writer, o options, layout, q string, args []any) error {
	db, err := openReader(ctx, o.dir, layout)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		return fmt.Errorf("%s plan: %w", layoutTitle(layout), err)
	}
	defer rows.Close()
	var lines []string
	search, scan := 0, 0
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return err
		}
		lines = append(lines, detail)
		search += strings.Count(detail, "SEARCH")
		scan += strings.Count(detail, "SCAN")
	}
	fmt.Fprintf(out, "\n%s (SEARCH %d, SCAN %d)\n```\n%s\n```\n", layoutTitle(layout), search, scan, strings.Join(lines, "\n"))
	return rows.Err()
}

// openShards opens an L3 reader over the first n daily files.
func openShards(ctx context.Context, root string, n int) (*sql.DB, error) {
	dir := layoutDir(root, layoutShards)
	db, err := repository.NewReadOnlyDB(filepath.Join(dir, configFile))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	files, err := dataFiles(dir)
	n = min(n, len(files))
	if err == nil {
		err = attachAll(ctx, db.DB, files[:n])
	}
	if err == nil {
		err = createUnionViews(ctx, db.DB, n)
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return db.DB, nil
}

// timeOpen times opening and closing a reader, runs times.
func timeOpen(runs int, open func() (*sql.DB, error)) (timings, error) {
	return measure(runs, func() error {
		db, err := open()
		if err == nil {
			err = db.Close()
		}
		return err
	})
}

// attachLimit attaches the config file under extra aliases until SQLite
// refuses, and returns how many attaches succeeded.
func attachLimit(ctx context.Context, root string) (int, error) {
	path := filepath.Join(layoutDir(root, layoutShards), configFile)
	db, err := repository.NewReadOnlyDB(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// SQLite caps attached databases (125 at most), so the loop ends.
	for n := 0; ; n++ {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE ? AS x%d", n), path); err != nil {
			return n, nil
		}
	}
}

func printAttachCost(ctx context.Context, out io.Writer, o options) error {
	files, err := dataFiles(layoutDir(o.dir, layoutShards))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "### Attach cost")
	fmt.Fprint(out, "\nTime to open a fresh read-only reader, including ATTACH and, for L3, the TEMP views.\n\n")
	fmt.Fprintln(out, "| reader | open p50 | open p95 |\n|---|---|---|")
	opens := []struct {
		name string
		open func() (*sql.DB, error)
	}{
		{"L2 split (2 data files)", func() (*sql.DB, error) { return openReader(ctx, o.dir, layoutSplit) }},
		{"L3 shards, 1 shard", func() (*sql.DB, error) { return openShards(ctx, o.dir, 1) }},
		{fmt.Sprintf("L3 shards, all %d shards", len(files)), func() (*sql.DB, error) { return openShards(ctx, o.dir, len(files)) }},
	}
	for _, op := range opens {
		t, err := timeOpen(max(o.runs, 1), op.open)
		if err != nil {
			return fmt.Errorf("%s: %w", op.name, err)
		}
		fmt.Fprintf(out, "| %s | %s | %s |\n", op.name, ms(t.pct(0.50)), ms(t.pct(0.95)))
	}
	n, err := attachLimit(ctx, o.dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nAttach limit: SQLite accepted %d attached databases on one connection before refusing, so 30 shards fit: %t.\n\n", n, n >= 30)
	return nil
}
