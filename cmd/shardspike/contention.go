package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// Production write rates the contention run replays. Spans flush every 125ms
// like the span writer; the heavy tables flush once a second like the batch
// consumers. Every loop is driven by a time.Ticker, so the rate is a hard cap.
const (
	contSpanTick       = 125 * time.Millisecond
	contSpansPerTick   = 8 // 64 spans/s, one 8-span trace per tick
	contHeavyTick      = time.Second
	contMetricsPerTick = 32
	contLogsPerTick    = 23
	contPromptsPerTick = 1
	contErrorsPerTick  = 1
	contLogBodyBytes   = 300
	contPromptBytes    = 2_700
	contCheckpointTick = 30 * time.Second
	contWALPollTick    = 100 * time.Millisecond
	contReadPause      = 10 * time.Millisecond
	contIdleMax        = 5 * time.Second
)

// contQuery is one span query the reader loop times.
type contQuery struct {
	name string
	run  func(*repository.Repository) error
}

// contRun is the outcome of one reader loop, idle or under write load.
type contRun struct {
	loaded      bool
	reads       map[string]timings
	spanInsert  *contRecorder
	heavyInsert *contRecorder
	// walPeak holds the largest WAL seen on the span writer's file [0] and the
	// heavy writer's file [1]. On L1 both are single.db.
	walPeak [2]atomic.Int64
}

// contRecorder collects batch latencies and row counts from several goroutines.
type contRecorder struct {
	mu   sync.Mutex
	t    timings
	rows int64
}

func (r *contRecorder) add(d time.Duration, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.t = append(r.t, d)
	r.rows += int64(rows)
}

func (r *contRecorder) snapshot() (timings, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append(timings(nil), r.t...), r.rows
}

// cmdContention answers whether span queries get faster when spans live in
// their own file, by timing them on L1 and L2 idle and under production-rate
// writes.
func cmdContention(ctx context.Context, o options, out io.Writer) error {
	traceID, err := contTraceID(ctx, o.dir)
	if err != nil {
		return err
	}
	queries := contQueries(o.window(), traceID)
	var runs []*contRun
	for _, l := range []string{layoutSingle, layoutSplit} {
		idle, err := contIdle(ctx, o, l, queries)
		if err != nil {
			return fmt.Errorf("contention %s idle: %w", l, err)
		}
		loaded, err := contLoaded(ctx, o, l, queries)
		if err != nil {
			return fmt.Errorf("contention %s loaded: %w", l, err)
		}
		runs = append(runs, idle, loaded)
	}
	printContention(out, o, runs)
	return nil
}

// contTraceID picks the newest project 1 trace from L1 before any write, so
// both layouts look up the same trace.
func contTraceID(ctx context.Context, root string) (string, error) {
	db, err := openReader(ctx, root, layoutSingle)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRowContext(ctx,
		"SELECT trace_id FROM trace_summaries ORDER BY project_id = 1 DESC, ingested_at DESC LIMIT 1").Scan(&id)
	if err != nil {
		return "", fmt.Errorf("pick trace id: %w", err)
	}
	return id, nil
}

// contQueries are the span queries the UI runs most: the trace list, one
// trace's detail and a 24h group-by, all over the last day of the dataset.
func contQueries(w window, traceID string) []contQuery {
	from, to := w.End.Add(-24*time.Hour), w.End
	return []contQuery{
		{"trace list", func(r *repository.Repository) error {
			_, err := r.SearchTraceSummaries(repository.SpanFilter{ProjectID: 1, From: from, To: to, Limit: 50}, 0)
			return err
		}},
		{"trace detail", func(r *repository.Repository) error {
			_, err := r.GetTraceByID(traceID)
			return err
		}},
		{"analyze 24h", func(r *repository.Repository) error {
			_, err := r.Analyze(context.Background(), repository.AnalyzeQuery{
				ProjectID: 1, From: from, To: to, GroupBy: []string{"service"},
				Calcs:    []repository.AnalyzeCalc{{Fn: repository.CalcCount}, {Fn: repository.CalcP95}},
				Desc:     true,
				Limit:    20,
				MaxSpans: 100_000,
			})
			return err
		}},
	}
}

// contIdle runs the reader loop with no writers, for min(5s, duration).
func contIdle(ctx context.Context, o options, layout string, queries []contQuery) (*contRun, error) {
	db, err := openReader(ctx, o.dir, layout)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rctx, cancel := context.WithTimeout(ctx, min(contIdleMax, o.duration))
	defer cancel()
	reads, err := contReadLoop(rctx, repository.NewReadOnlyRepository(db), queries)
	return &contRun{reads: reads}, err
}

// contLoaded runs the reader loop for the whole duration while the writers
// insert at production rates. It returns only after every goroutine stopped.
func contLoaded(ctx context.Context, o options, layout string, queries []contQuery) (*contRun, error) {
	ws, err := openContWriters(o.dir, layout)
	if err != nil {
		return nil, err
	}
	defer ws.close()
	db, err := openReader(ctx, o.dir, layout)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	run := &contRun{loaded: true, spanInsert: &contRecorder{}, heavyInsert: &contRecorder{}}
	rctx, cancel := context.WithTimeout(ctx, o.duration)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	ws.start(rctx, &wg, errs, run)
	reads, rerr := contReadLoop(rctx, repository.NewReadOnlyRepository(db), queries)
	cancel()
	wg.Wait()
	close(errs)
	run.reads = reads
	for err := range errs {
		rerr = errors.Join(rerr, err)
	}
	return run, rerr
}

// contReadLoop runs every query back to back with a short pause between
// rounds until ctx ends. A query that fails because ctx ended is not an error.
func contReadLoop(ctx context.Context, repo *repository.Repository, queries []contQuery) (map[string]timings, error) {
	out := map[string]timings{}
	for ctx.Err() == nil {
		for _, q := range queries {
			start := time.Now()
			if err := q.run(repo); err != nil {
				if ctx.Err() != nil {
					return out, nil
				}
				return out, fmt.Errorf("%s: %w", q.name, err)
			}
			out[q.name] = append(out[q.name], time.Since(start))
		}
		select {
		case <-ctx.Done():
		case <-time.After(contReadPause):
		}
	}
	return out, nil
}

// contWriters holds the writer connections of one layout: on L1 one shared
// *DB for every table, as in production; on L2 one *DB per file.
type contWriters struct {
	dbs   []*repository.DB
	paths [2]string // span writer file, heavy writer file
	spans *repository.Repository
	heavy *repository.Repository
}

func openContWriters(root, layout string) (*contWriters, error) {
	dir := layoutDir(root, layout)
	if layout == layoutSingle {
		path := filepath.Join(dir, singleFile)
		db, err := repository.NewDB(path)
		if err != nil {
			return nil, err
		}
		repo := repository.NewRepository(db.DB)
		return &contWriters{dbs: []*repository.DB{db}, paths: [2]string{path, path}, spans: repo, heavy: repo}, nil
	}
	spansPath, heavyPath := filepath.Join(dir, "spans.db"), filepath.Join(dir, "heavy.db")
	sdb, err := repository.NewDB(spansPath)
	if err != nil {
		return nil, err
	}
	hdb, err := repository.NewDB(heavyPath)
	if err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return &contWriters{
		dbs: []*repository.DB{sdb, hdb}, paths: [2]string{spansPath, heavyPath},
		spans: repository.NewRepository(sdb.DB), heavy: repository.NewRepository(hdb.DB),
	}, nil
}

// close merges each WAL into its file before closing, so the next command
// starts from a checkpointed file as it would after a clean shutdown.
func (w *contWriters) close() {
	log := slog.New(slog.DiscardHandler)
	for _, db := range w.dbs {
		db.FinalCheckpoint(log)
		_ = db.Close()
	}
}

// start launches the checkpoint loops, the WAL pollers and the five writer
// loops. Each goroutine is registered on wg and ends when ctx ends.
func (w *contWriters) start(ctx context.Context, wg *sync.WaitGroup, errs chan<- error, run *contRun) {
	log := slog.New(slog.DiscardHandler)
	for _, db := range w.dbs {
		contGo(wg, func() { db.RunPeriodicCheckpoint(ctx, contCheckpointTick, log) })
	}
	for i, p := range w.paths {
		contGo(wg, func() { contPollWAL(ctx, p+"-wal", &run.walPeak[i]) })
	}
	for i, wr := range w.writerLoops() {
		rec := run.heavyInsert
		if wr.span {
			rec = run.spanInsert
		}
		g := newGenerator(prodProfile(), window{}, spikeSeed+uint64(i)+1)
		contGo(wg, func() {
			if err := contTickLoop(ctx, wr.every, rec, func(now time.Time) (int, error) { return wr.insert(ctx, g, now) }); err != nil {
				errs <- err
			}
		})
	}
}

func contGo(wg *sync.WaitGroup, fn func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		fn()
	}()
}

// contWriterLoop is one rate-limited insert loop: every tick it builds a batch
// and inserts it, returning how many rows it wrote.
type contWriterLoop struct {
	span   bool
	every  time.Duration
	insert func(context.Context, *generator, time.Time) (int, error)
}

func (w *contWriters) writerLoops() []contWriterLoop {
	return []contWriterLoop{
		{span: true, every: contSpanTick, insert: func(ctx context.Context, g *generator, now time.Time) (int, error) {
			return contSpansPerTick, w.spans.InsertSpansContext(ctx, contSpans(g, now, contSpansPerTick))
		}},
		{every: contHeavyTick, insert: func(ctx context.Context, g *generator, now time.Time) (int, error) {
			return contMetricsPerTick, w.heavy.InsertMetrics(ctx, contBatch(contMetricsPerTick, g, now, contMetric))
		}},
		{every: contHeavyTick, insert: func(ctx context.Context, g *generator, now time.Time) (int, error) {
			return contLogsPerTick, w.heavy.InsertLogs(ctx, contBatch(contLogsPerTick, g, now, contLog))
		}},
		{every: contHeavyTick, insert: func(_ context.Context, g *generator, now time.Time) (int, error) {
			return contPromptsPerTick, w.heavy.InsertPromptRecords(contBatch(contPromptsPerTick, g, now, contPrompt))
		}},
		{every: contHeavyTick, insert: func(_ context.Context, g *generator, now time.Time) (int, error) {
			return contErrorsPerTick, w.heavy.InsertErrorSamples(contBatch(contErrorsPerTick, g, now, contErrorSample))
		}},
	}
}

// contTickLoop calls fn once per tick until ctx ends and records the time from
// tick to commit. A ticker drops ticks while fn is still running, so a slow
// insert lowers the rate and never queues a burst.
func contTickLoop(ctx context.Context, every time.Duration, rec *contRecorder, fn func(time.Time) (int, error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case tick := <-t.C:
			n, err := fn(tick)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			rec.add(time.Since(tick), n)
		}
	}
}

// contPollWAL records the largest size the WAL file at path reaches.
func contPollWAL(ctx context.Context, path string, peak *atomic.Int64) {
	t := time.NewTicker(contWALPollTick)
	defer t.Stop()
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > peak.Load() {
			peak.Store(fi.Size())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func contBatch[T any](n int, g *generator, now time.Time, mk func(*generator, time.Time) T) []T {
	out := make([]T, n)
	for i := range out {
		out[i] = mk(g, now)
	}
	return out
}

// contSpans builds one trace of n spans: a root and n-1 children.
func contSpans(g *generator, now time.Time, n int) []repository.Span {
	project, traceID, rootID := g.project(), g.hexID(32), g.hexID(16)
	svc := services[g.rng.IntN(len(services))]
	out := make([]repository.Span, n)
	for i := range out {
		s := repository.Span{
			ProjectID: project, TraceID: traceID, SpanID: g.hexID(16), ParentSpanID: rootID,
			Name: operations[3+g.rng.IntN(len(operations)-3)], Service: svc, Kind: "server", Status: "ok",
			StartTimeUs: now.UnixMicro(), DurationUs: int64(1_000 + g.rng.IntN(80_000)),
			Attributes: fmt.Sprintf(`{"http.response.status_code":200,"user.id":"u%d"}`, g.rng.IntN(5_000)),
			Events:     "[]",
		}
		if i == 0 {
			s.SpanID, s.ParentSpanID, s.Name = rootID, "", operations[g.rng.IntN(3)]
		}
		out[i] = s
	}
	return out
}

func contMetric(g *generator, now time.Time) model.MetricRecord {
	return model.MetricRecord{
		ProjectID: g.project(), Name: metricKeys[g.rng.IntN(len(metricKeys))], Unit: "ms",
		Type: model.MetricTypeGauge, TimeUnixNano: uint64(now.UnixNano()), Value: g.rng.Float64() * 100, Count: 1,
		Attributes: json.RawMessage(fmt.Sprintf(`{"service.name":%q,"host.name":"node-%d"}`,
			services[g.rng.IntN(len(services))], g.rng.IntN(12))),
	}
}

func contLog(g *generator, now time.Time) model.LogRecord {
	return model.LogRecord{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16), SeverityNumber: 9, SeverityText: "INFO",
		TimeUnixNano: uint64(now.UnixNano()), Body: g.text(contLogBodyBytes),
		Attributes: json.RawMessage(fmt.Sprintf(`{"service.name":%q}`, services[g.rng.IntN(len(services))])),
	}
}

func contPrompt(g *generator, now time.Time) repository.PromptRecord {
	in, out := int64(200+g.rng.IntN(2_000)), int64(50+g.rng.IntN(800))
	return repository.PromptRecord{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16), Service: "llm", Name: "chat",
		GenAISystem: "openai", Model: models[g.rng.IntN(len(models))],
		PromptBody: g.text(contPromptBytes), ResponseBody: g.text(contPromptBytes),
		InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: float64(in+out) * 1e-6,
		DurationUs: int64(200_000 + g.rng.IntN(4_000_000)), StartTimeUs: now.UnixMicro(), Status: "ok",
	}
}

func contErrorSample(g *generator, now time.Time) repository.Span {
	return repository.Span{
		ProjectID: g.project(), TraceID: g.hexID(32), SpanID: g.hexID(16),
		Name: operations[g.rng.IntN(len(operations))], Service: services[g.rng.IntN(len(services))],
		Kind: "server", Status: "error", StartTimeUs: now.UnixMicro(), DurationUs: int64(g.rng.IntN(500_000)),
		Attributes: `{"http.response.status_code":500}`,
		Events:     fmt.Sprintf(`[{"name":"exception","attributes":{"exception.message":%q}}]`, g.text(600)),
	}
}

// contRow is one table row: a label and how to render it for a run.
type contRow struct {
	label string
	write bool
	cell  func(*contRun) string
}

func contRows(queries []string) []contRow {
	var rows []contRow
	for _, q := range queries {
		for _, p := range []struct {
			name string
			q    float64
		}{{"p50", 0.50}, {"p95", 0.95}, {"p99", 0.99}} {
			rows = append(rows, contRow{label: q + " " + p.name, cell: func(r *contRun) string { return ms(r.reads[q].pct(p.q)) }})
		}
	}
	rows = append(rows, contRow{label: "read rounds (trace list n)", cell: func(r *contRun) string {
		return fmt.Sprint(len(r.reads[queries[0]]))
	}})
	return append(rows, contWriteRows()...)
}

// contWriteRows only apply to loaded runs; idle cells print "-".
func contWriteRows() []contRow {
	// pct(1) is the largest sample, so q=1 renders the max.
	ins := func(pick func(*contRun) *contRecorder, q float64) func(*contRun) string {
		return func(r *contRun) string { t, _ := pick(r).snapshot(); return ms(t.pct(q)) }
	}
	span := func(r *contRun) *contRecorder { return r.spanInsert }
	heavy := func(r *contRun) *contRecorder { return r.heavyInsert }
	rows := func(pick func(*contRun) *contRecorder) func(*contRun) string {
		return func(r *contRun) string { _, n := pick(r).snapshot(); return fmt.Sprint(n) }
	}
	wal := func(i int) func(*contRun) string {
		return func(r *contRun) string { return fmt.Sprintf("%.2f MB", float64(r.walPeak[i].Load())/1e6) }
	}
	return []contRow{
		{"span insert p50", true, ins(span, 0.50)}, {"span insert p99", true, ins(span, 0.99)},
		{"span insert max", true, ins(span, 1)},
		{"heavy insert p50", true, ins(heavy, 0.50)}, {"heavy insert p99", true, ins(heavy, 0.99)},
		{"spans written", true, rows(span)}, {"heavy rows written", true, rows(heavy)},
		{"WAL peak, span writer file", true, wal(0)}, {"WAL peak, heavy writer file", true, wal(1)},
	}
}

// printContention writes the markdown section. runs alternate idle, loaded
// for L1 then L2.
func printContention(out io.Writer, o options, runs []*contRun) {
	fmt.Fprintf(out, "\n## Span reads under write load (L1 vs L2)\n\n")
	fmt.Fprintf(out, "Loaded runs last %s, idle baselines %s. Writes: spans %d/s (%d per %s), metrics %d/s, logs %d/s (%d B bodies), prompts %d/s (%.1f KB bodies), error samples %d/s, heavy tables flushed every %s, WAL checkpoint every %s per writer file. L1 shares one writer connection; L2 has one per file (spans.db, heavy.db).\n\n",
		o.duration, min(contIdleMax, o.duration), contSpansPerTick*int(time.Second/contSpanTick), contSpansPerTick, contSpanTick,
		contMetricsPerTick, contLogsPerTick, contLogBodyBytes, contPromptsPerTick, float64(contPromptBytes)/1000,
		contErrorsPerTick, contHeavyTick, contCheckpointTick)
	fmt.Fprintln(out, "| measure | L1 single idle | L1 single loaded | L2 split idle | L2 split loaded |")
	fmt.Fprintln(out, "|---|---|---|---|---|")
	for _, row := range contRows([]string{"trace list", "trace detail", "analyze 24h"}) {
		fmt.Fprintf(out, "| %s |", row.label)
		for _, r := range runs {
			fmt.Fprintf(out, " %s |", contCell(row, r))
		}
		fmt.Fprintln(out)
	}
}

// contCell renders a write-side row as "-" for idle runs, which have no writers.
func contCell(row contRow, r *contRun) string {
	if row.write && !r.loaded {
		return "-"
	}
	return row.cell(r)
}
