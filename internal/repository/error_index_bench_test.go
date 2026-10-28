package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// BenchmarkErrorTraceIndexWriteCost measures what the partial error-trace
// indexes add to the writer's span and error-sample inserts: 500-row batches
// with 1% error rows into a file database that already holds 100,000 spans
// and error samples, with the indexes and with them dropped.
//
//	go test ./internal/repository -run '^$' -bench ErrorTraceIndexWriteCost -benchtime 200x
func BenchmarkErrorTraceIndexWriteCost(b *testing.B) {
	repos := map[string]*Repository{
		"without": errorIndexBenchRepo(b, false),
		"with":    errorIndexBenchRepo(b, true),
	}
	inserts := map[string]func(*Repository, []Span) error{
		"spans":         (*Repository).InsertSpans,
		"error_samples": (*Repository).InsertErrorSamples,
	}
	// Variants alternate per table so neither database always runs second.
	for _, table := range []string{"spans", "error_samples"} {
		for _, variant := range []string{"without", "with"} {
			b.Run(table+"/"+variant, func(b *testing.B) {
				for i := range b.N {
					if err := inserts[table](repos[variant], errorIndexBatch(fmt.Sprintf("%s%d", table, i))); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func errorIndexBenchRepo(b *testing.B, withIndexes bool) *Repository {
	b.Helper()
	db, err := NewDB(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	if err := MigrateSingleFile(context.Background(), db.DB); err != nil {
		b.Fatal(err)
	}
	if !withIndexes {
		if _, err := db.Exec(`DROP INDEX idx_spans_error_trace; DROP INDEX idx_error_samples_error_trace`); err != nil {
			b.Fatal(err)
		}
	}
	repo := NewRepository(db.DB)
	for i := range 200 {
		batch := errorIndexBatch(fmt.Sprintf("fill%d", i))
		if err := repo.InsertSpans(batch); err != nil {
			b.Fatal(err)
		}
		if err := repo.InsertErrorSamples(batch); err != nil {
			b.Fatal(err)
		}
	}
	return repo
}

// errorIndexBatch is 500 spans of 50 traces, five of them errors.
func errorIndexBatch(prefix string) []Span {
	spans := make([]Span, 0, 500)
	for i := range 500 {
		status := "ok"
		if i%100 == 0 {
			status = "error"
		}
		s := makeSpan(1, fmt.Sprintf("%s-t%d", prefix, i/10), fmt.Sprintf("%s-s%d", prefix, i), "api", fmt.Sprintf("op%d", i%20), status, 1000)
		s.Attributes = `{"http.route":"/api/items/{id}","db.statement":"SELECT * FROM items WHERE id = ?","user.agent":"Mozilla/5.0"}`
		spans = append(spans, s)
	}
	return spans
}
