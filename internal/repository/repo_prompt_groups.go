package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// PromptGroup is the prompts in a range for one operation name, model and
// service: call and error counts, token and cost sums, and a duration
// histogram for percentiles.
type PromptGroup struct {
	Name         string
	Model        string
	Service      string
	GenAISystem  string
	Count        int64
	ErrorCount   int64
	TotalTimeUs  int64
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	Hist         spanhist.Hist
}

// QueryPromptGroups folds the prompts that match f (Limit and Offset are
// ignored) into one group per name, model and service. SQLite returns a row
// per group and duration bin and reads no prompt or response bodies, so the
// whole range counts, however many prompts it holds. Over a sharded family
// each segment is grouped on its own and the groups are merged here.
func (r *Repository) QueryPromptGroups(ctx context.Context, f PromptFilter) ([]PromptGroup, error) {
	ctx, cancel := r.readContext(ctx)
	defer cancel()
	where, args := f.where()
	query := func(table string) string {
		q := fmt.Sprintf(`
			SELECT name, model, service, MAX(gen_ai_system), %s AS bin, COUNT(*),
			       SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END),
			       MIN(duration_us), MAX(duration_us), SUM(duration_us),
			       SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
			FROM %s`, spanhist.IndexSQL("duration_us"), table)
		if len(where) > 0 {
			q += " WHERE " + strings.Join(where, " AND ")
		}
		return q + " GROUP BY name, model, service, bin"
	}

	g := promptGrouper{byKey: map[promptGroupKey]*PromptGroup{}}
	if p := r.shardReaders.pool(FamilyPrompts); p != nil {
		for _, schema := range p.segments {
			if err := g.read(ctx, p.db.DB, query(schema+".prompt_records"), args); err != nil {
				return nil, err
			}
		}
	} else if err := g.read(ctx, r.db, query("prompt_records"), args); err != nil {
		return nil, err
	}
	out := make([]PromptGroup, 0, len(g.order))
	for _, k := range g.order {
		out = append(out, *g.byKey[k])
	}
	return out, nil
}

type promptGroupKey struct{ name, model, service string }

// promptGrouper merges per-bin rows into groups, keeping first-seen order.
type promptGrouper struct {
	byKey map[promptGroupKey]*PromptGroup
	order []promptGroupKey
}

func (g *promptGrouper) read(ctx context.Context, db *sql.DB, query string, args []any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row PromptGroup
		var b spanhist.Bin
		if err := rows.Scan(&row.Name, &row.Model, &row.Service, &row.GenAISystem, &b.Index, &b.Count,
			&row.ErrorCount, &b.Min, &b.Max, &row.TotalTimeUs,
			&row.InputTokens, &row.OutputTokens, &row.CostUSD); err != nil {
			return err
		}
		g.add(row, b)
	}
	return rows.Err()
}

func (g *promptGrouper) add(row PromptGroup, b spanhist.Bin) {
	k := promptGroupKey{row.Name, row.Model, row.Service}
	cur, ok := g.byKey[k]
	if !ok {
		cur = &PromptGroup{Name: row.Name, Model: row.Model, Service: row.Service}
		g.byKey[k] = cur
		g.order = append(g.order, k)
	}
	cur.GenAISystem = max(cur.GenAISystem, row.GenAISystem)
	cur.Count += b.Count
	cur.ErrorCount += row.ErrorCount
	cur.TotalTimeUs += row.TotalTimeUs
	cur.InputTokens += row.InputTokens
	cur.OutputTokens += row.OutputTokens
	cur.CostUSD += row.CostUSD
	cur.Hist.Add(b)
}
