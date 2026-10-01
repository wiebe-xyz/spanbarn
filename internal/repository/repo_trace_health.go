package repository

import (
	"context"
	"database/sql"
	"time"
)

// HealthWindow is the project and ingested_at range every trace health query is
// bound by. The orphan and single-span checks join spans back to themselves,
// which is only affordable on a bounded window, so callers must set all of it.
type HealthWindow struct {
	ProjectID int64
	From, To  time.Time
	Limit     int
}

func (w HealthWindow) limit() int {
	if w.Limit <= 0 {
		return 100
	}
	return w.Limit
}

// structureFromColumns converts the nullable summary columns.
func structureFromColumns(hasRoot, orphans sql.NullInt64) (*bool, int) {
	if !hasRoot.Valid {
		return nil, int(orphans.Int64)
	}
	v := hasRoot.Int64 == 1
	return &v, int(orphans.Int64)
}

// appendStructureWhere adds the has_root and orphan filters of a trace list.
func (f SpanFilter) appendStructureWhere(where []string) []string {
	if f.HasRoot != nil {
		if *f.HasRoot {
			where = append(where, "has_root = 1")
		} else {
			where = append(where, "has_root = 0")
		}
	}
	if f.HasOrphans {
		where = append(where, "orphan_count > 0")
	}
	return where
}

// OrphanSpanGroup is a set of orphan spans sharing name, kind and service.
type OrphanSpanGroup struct {
	Name          string
	Kind          string
	Service       string
	Count         int64
	SampleTraceID string
}

// QueryOrphanSpanGroups groups the spans in the window whose parent_span_id is
// set but matches no span in the same trace. The parent lookup is not bound by
// the window (a parent may have been ingested earlier) and seeks idx_spans_trace.
func (r *Repository) QueryOrphanSpanGroups(ctx context.Context, w HealthWindow) ([]OrphanSpanGroup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.name, s.kind, s.service, COUNT(*), MIN(s.trace_id)
		FROM spans s
		WHERE s.project_id = ? AND s.ingested_at >= ? AND s.ingested_at <= ?
		  AND s.parent_span_id IS NOT NULL AND s.parent_span_id != ''
		  AND NOT EXISTS (SELECT 1 FROM spans p
		                  WHERE p.trace_id = s.trace_id AND p.project_id = s.project_id
		                    AND p.span_id = s.parent_span_id)
		GROUP BY s.name, s.kind, s.service
		ORDER BY COUNT(*) DESC, s.name
		LIMIT ?`, w.ProjectID, w.From, w.To, w.limit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrphanSpanGroup
	for rows.Next() {
		var g OrphanSpanGroup
		if err := rows.Scan(&g.Name, &g.Kind, &g.Service, &g.Count, &g.SampleTraceID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SingleSpanTraceGroup is a set of one-span traces sharing the span's name.
type SingleSpanTraceGroup struct {
	Name          string
	Service       string
	Count         int64
	SampleTraceID string
}

// QuerySingleSpanTraceGroups groups the traces in the window whose summary says
// span_count = 1 by the name and service of that one span.
func (r *Repository) QuerySingleSpanTraceGroups(ctx context.Context, w HealthWindow) ([]SingleSpanTraceGroup, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.name, s.service, COUNT(*), MIN(t.trace_id)
		FROM trace_summaries t
		JOIN spans s ON s.trace_id = t.trace_id AND s.project_id = t.project_id
		WHERE t.project_id = ? AND t.ingested_at >= ? AND t.ingested_at <= ?
		  AND t.span_count = 1
		GROUP BY s.name, s.service
		ORDER BY COUNT(*) DESC, s.name
		LIMIT ?`, w.ProjectID, w.From, w.To, w.limit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SingleSpanTraceGroup
	for rows.Next() {
		var g SingleSpanTraceGroup
		if err := rows.Scan(&g.Name, &g.Service, &g.Count, &g.SampleTraceID); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SpanNameSummary is the per-name count and how many of those spans are roots.
type SpanNameSummary struct {
	Name      string
	Count     int64
	RootCount int64
}

// QuerySpanNameSummary counts spans per name in the window, with RootCount the
// number of those without a parent. A name whose RootCount equals its Count is
// an entry point; a RootCount of zero is an internal helper.
func (r *Repository) QuerySpanNameSummary(ctx context.Context, w HealthWindow) ([]SpanNameSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	rows, err := r.db.QueryContext(ctx, `
		SELECT name, COUNT(*),
		       SUM(CASE WHEN parent_span_id IS NULL OR parent_span_id = '' THEN 1 ELSE 0 END)
		FROM spans
		WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?
		GROUP BY name
		ORDER BY COUNT(*) DESC, name
		LIMIT ?`, w.ProjectID, w.From, w.To, w.limit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SpanNameSummary
	for rows.Next() {
		var g SpanNameSummary
		if err := rows.Scan(&g.Name, &g.Count, &g.RootCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RootlessTraceCount counts the summaries in the window with has_root = 0.
func (r *Repository) RootlessTraceCount(ctx context.Context, w HealthWindow) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()
	var n int64
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM trace_summaries
		WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ? AND has_root = 0`,
		w.ProjectID, w.From, w.To).Scan(&n)
	return n, err
}
