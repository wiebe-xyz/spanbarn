package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// insertBatch is the number of rows per transaction while generating. Large
// enough that commit cost does not dominate, small enough to keep the WAL short.
const insertBatch = 5_000

// generator produces deterministic synthetic rows. The same seed and profile
// always produce the same dataset, so two runs measure the same data.
type generator struct {
	p   profile
	w   window
	rng *rand.Rand
}

func newGenerator(p profile, w window, seed uint64) *generator {
	return &generator{p: p, w: w, rng: rand.New(rand.NewPCG(seed, seed^0x5eed))}
}

var (
	services   = []string{"api", "web", "worker", "ingest", "billing", "search"}
	operations = []string{"GET /v1/items", "POST /v1/items", "GET /v1/search", "db.query", "cache.get", "http.client", "render", "queue.publish"}
	metricKeys = []string{"http.server.duration", "process.cpu.time", "runtime.go.mem.heap_alloc", "db.client.connections.usage", "queue.depth"}
	models     = []string{"gpt-4o-mini", "claude-sonnet", "llama-3-70b"}
)

// project picks a project with the production skew: one project carries ~86%
// of the rows, the rest share the remainder.
func (g *generator) project() int64 {
	if g.rng.Float64() < 0.86 || g.p.Projects < 2 {
		return 1
	}
	return int64(2 + g.rng.IntN(g.p.Projects-1))
}

// instant returns a uniformly random time inside the window.
func (g *generator) instant() time.Time {
	span := g.w.End.Sub(g.w.Start)
	return g.w.Start.Add(time.Duration(g.rng.Int64N(int64(span))))
}

func (g *generator) hexID(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.rng.IntN(16)]
	}
	return string(b)
}

func (g *generator) text(n int) string {
	const words = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor "
	var sb strings.Builder
	for sb.Len() < n {
		sb.WriteString(words[g.rng.IntN(len(words)/2):])
	}
	return sb.String()[:n]
}

// insertRows runs one prepared INSERT per row, n rows, committing every
// insertBatch rows. row fills the values for row i.
func insertRows(ctx context.Context, db *sql.DB, table string, cols []string, n int, row func(i int) []any) error {
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "),
		strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", "))
	for done := 0; done < n; {
		end := min(done+insertBatch, n)
		if err := insertChunk(ctx, db, q, done, end, row); err != nil {
			return fmt.Errorf("insert %s: %w", table, err)
		}
		done = end
	}
	return nil
}

func insertChunk(ctx context.Context, db *sql.DB, q string, from, to int, row func(i int) []any) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := from; i < to; i++ {
		if _, err := stmt.ExecContext(ctx, row(i)...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// generate fills a migrated database with the whole synthetic dataset.
func (g *generator) generate(ctx context.Context, db *sql.DB) error {
	steps := []func(context.Context, *sql.DB) error{
		g.genProjects, g.genTraces, g.genLogs, g.genMetrics, g.genPrompts, g.genErrorSamples,
	}
	for _, step := range steps {
		if err := step(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) genProjects(ctx context.Context, db *sql.DB) error {
	return insertRows(ctx, db, "projects", []string{"id", "name", "slug"}, g.p.Projects, func(i int) []any {
		return []any{i + 1, fmt.Sprintf("project %d", i+1), fmt.Sprintf("project-%d", i+1)}
	})
}

var spanCols = []string{"project_id", "trace_id", "span_id", "parent_span_id", "name", "service", "kind",
	"status", "start_time_us", "duration_us", "attributes", "ingested_at"}

var summaryCols = []string{"project_id", "trace_id", "root_name", "root_service", "root_duration_us",
	"start_time_us", "span_count", "has_error", "ingested_at", "has_root", "orphan_count"}

// genTraces writes spans and the matching trace_summaries rows. Spans of one
// trace are ingested a few seconds apart, so traces that start just before
// midnight leave spans in two daily shards, as they do in production.
func (g *generator) genTraces(ctx context.Context, db *sql.DB) error {
	per := g.p.SpansPerTrace
	traces := g.p.Spans / per
	summaries := make([][]any, 0, traces)
	var current [][]any
	next := func(i int) []any {
		if i%per == 0 {
			var summary []any
			current, summary = g.trace(current[:0])
			summaries = append(summaries, summary)
		}
		return current[i%per]
	}
	if err := insertRows(ctx, db, "spans", spanCols, traces*per, next); err != nil {
		return err
	}
	return insertRows(ctx, db, "trace_summaries", summaryCols, len(summaries), func(i int) []any { return summaries[i] })
}

func (g *generator) trace(buf [][]any) ([][]any, []any) {
	project, traceID, start := g.project(), g.hexID(32), g.instant()
	svc := services[g.rng.IntN(len(services))]
	rootName := operations[g.rng.IntN(3)]
	rootDur := int64(1_000 + g.rng.ExpFloat64()*80_000)
	hasError := g.rng.Float64() < 0.02
	rootID := g.hexID(16)
	for i := range g.p.SpansPerTrace {
		name, parent, dur, status := rootName, any(nil), rootDur, "ok"
		if i > 0 {
			name, parent, dur = operations[3+g.rng.IntN(len(operations)-3)], rootID, rootDur/int64(2+i)
		}
		if hasError && i == g.p.SpansPerTrace-1 {
			status = "error"
		}
		ingested := start.Add(time.Duration(g.rng.IntN(5_000)) * time.Millisecond)
		attrs := fmt.Sprintf(`{"http.response.status_code":%d,"user.id":"u%d","region":"eu-%d"}`,
			200+300*boolInt(status == "error"), g.rng.IntN(5_000), g.rng.IntN(4))
		spanID := rootID
		if i > 0 {
			spanID = g.hexID(16)
		}
		buf = append(buf, []any{project, traceID, spanID, parent, name, svc, "server", status,
			start.UnixMicro(), dur, attrs, sqlTime(ingested)})
	}
	summary := []any{project, traceID, rootName, svc, rootDur, start.UnixMicro(), g.p.SpansPerTrace,
		boolInt(hasError), sqlTime(start), 1, 0}
	return buf, summary
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (g *generator) genLogs(ctx context.Context, db *sql.DB) error {
	cols := []string{"project_id", "trace_id", "span_id", "severity_number", "severity_text",
		"time_unix_nano", "body", "attributes", "ingested_at"}
	return insertRows(ctx, db, "logs", cols, g.p.Logs, func(int) []any {
		t := g.instant()
		sev, txt := 9, "INFO"
		if g.rng.Float64() < 0.05 {
			sev, txt = 17, "ERROR"
		}
		return []any{g.project(), g.hexID(32), g.hexID(16), sev, txt, t.UnixNano(),
			g.text(g.p.LogBodyBytes), fmt.Sprintf(`{"service.name":"%s","logger":"app"}`, services[g.rng.IntN(len(services))]),
			sqlTime(t)}
	})
}

func (g *generator) genMetrics(ctx context.Context, db *sql.DB) error {
	cols := []string{"project_id", "name", "unit", "type", "time_unix_nano", "value", "count", "attributes", "ingested_at"}
	return insertRows(ctx, db, "metrics", cols, g.p.Metrics, func(int) []any {
		t := g.instant()
		attrs := fmt.Sprintf(`{"service.name":"%s","host.name":"node-%d","http.route":"/v1/r%d"}`,
			services[g.rng.IntN(len(services))], g.rng.IntN(12), g.rng.IntN(40))
		return []any{g.project(), metricKeys[g.rng.IntN(len(metricKeys))], "ms", "gauge", t.UnixNano(),
			g.rng.Float64() * 100, 1, attrs, sqlTime(t)}
	})
}

func (g *generator) genPrompts(ctx context.Context, db *sql.DB) error {
	cols := []string{"project_id", "trace_id", "span_id", "service", "name", "gen_ai_system", "model",
		"prompt_body", "response_body", "input_tokens", "output_tokens", "total_tokens", "cost_usd",
		"duration_us", "start_time_us", "ingested_at"}
	return insertRows(ctx, db, "prompt_records", cols, g.p.Prompts, func(int) []any {
		t := g.instant()
		in, out := 200+g.rng.IntN(2_000), 50+g.rng.IntN(800)
		return []any{g.project(), g.hexID(32), g.hexID(16), "llm", "chat", "openai", models[g.rng.IntN(len(models))],
			g.text(g.p.PromptBodyBytes), g.text(g.p.PromptBodyBytes), in, out, in + out, float64(in+out) * 1e-6,
			int64(200_000 + g.rng.IntN(4_000_000)), t.UnixMicro(), sqlTime(t)}
	})
}

func (g *generator) genErrorSamples(ctx context.Context, db *sql.DB) error {
	cols := []string{"project_id", "trace_id", "span_id", "name", "service", "status", "start_time_us",
		"duration_us", "attributes", "events", "ingested_at", "sampled_at"}
	return insertRows(ctx, db, "error_samples", cols, g.p.ErrorSamples, func(int) []any {
		t := g.instant()
		events := fmt.Sprintf(`[{"name":"exception","attributes":{"exception.message":%q}}]`, g.text(600))
		return []any{g.project(), g.hexID(32), g.hexID(16), operations[g.rng.IntN(len(operations))],
			services[g.rng.IntN(len(services))], "error", t.UnixMicro(), int64(g.rng.IntN(500_000)),
			`{"http.response.status_code":500}`, events, sqlTime(t), sqlTime(t)}
	})
}
