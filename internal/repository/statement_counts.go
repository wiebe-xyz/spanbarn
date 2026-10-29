package repository

import (
	"context"
	"sort"
	"sync"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
)

// StatementCounts counts the database calls made under a context in place of
// one otelsql span per call.
//
// Background work such as a retention cycle runs thousands of statements. As
// spans, one cycle on 2026-10-29 carried 2996 of them (1748 sqlite.exec, 793
// selects, 348 deletes), all stored, which spent disk on the work that exists
// to free it. The counts go onto the caller's own span as attributes instead.
type StatementCounts struct {
	mu sync.Mutex
	n  map[string]int64
}

type statementCountsKey struct{}

// WithStatementCounts returns a context under which otelsql creates no span
// and counts each call under its span name instead, and the counts it fills.
func WithStatementCounts(ctx context.Context) (context.Context, *StatementCounts) {
	c := &StatementCounts{n: make(map[string]int64)}
	return context.WithValue(ctx, statementCountsKey{}, c), c
}

func statementCountsFrom(ctx context.Context) *StatementCounts {
	c, _ := ctx.Value(statementCountsKey{}).(*StatementCounts)
	return c
}

// count records one call: under its statement's span name, or under the
// otelsql method for calls without a query (begin, commit, rows).
func (c *StatementCounts) count(ctx context.Context, method otelsql.Method, query string) {
	name := string(method)
	if query != "" {
		name = sqliteSpanName(ctx, method, query)
	}
	c.mu.Lock()
	c.n[name]++
	c.mu.Unlock()
}

// Attributes returns db.calls with the total and one db.calls.<name> per name.
func (c *StatementCounts) Attributes() []attribute.KeyValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.n))
	var total int64
	for name, n := range c.n {
		names = append(names, name)
		total += n
	}
	sort.Strings(names)
	attrs := make([]attribute.KeyValue, 0, len(names)+1)
	attrs = append(attrs, attribute.Int64("db.calls", total))
	for _, name := range names {
		attrs = append(attrs, attribute.Int64("db.calls."+name, c.n[name]))
	}
	return attrs
}
