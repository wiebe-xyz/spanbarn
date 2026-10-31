package repository

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// A chunk that fails reports the rows from it on, in key order, and the chunks
// before it stay written.
func TestUpsertAggregatesReportsUnwrittenRows(t *testing.T) {
	repo := setupTestDB(t)
	failOp := fmt.Sprintf("op%05d", aggregateChunk) // first key of the second chunk
	if _, err := repo.writer(FamilySpans).Exec(`CREATE TRIGGER fail_second_chunk BEFORE INSERT ON aggregates
		WHEN NEW.operation = '` + failOp + `' BEGIN SELECT RAISE(ABORT, 'chunk failed'); END`); err != nil {
		t.Fatal(err)
	}
	bucket := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	rows := make([]Aggregate, 2*aggregateChunk+5)
	for i := range rows {
		rows[i] = Aggregate{ProjectID: 1, Service: "svc", Operation: fmt.Sprintf("op%05d", i), Kind: "server", Bucket: bucket, Count: 1}
	}

	err := repo.UpsertAggregates(rows)
	var unwritten *UnwrittenRowsError[Aggregate]
	if !errors.As(err, &unwritten) {
		t.Fatalf("UpsertAggregates with a failing chunk returned %v, want an UnwrittenRowsError", err)
	}
	if len(unwritten.Rows) != aggregateChunk+5 || unwritten.Rows[0].Operation != failOp {
		t.Errorf("%d unwritten rows from %s, want %d from %s", len(unwritten.Rows), unwritten.Rows[0].Operation, aggregateChunk+5, failOp)
	}
	var stored int
	if err := repo.writer(FamilySpans).QueryRow(`SELECT count(*) FROM aggregates`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != aggregateChunk {
		t.Errorf("%d rows stored, want the first chunk's %d", stored, aggregateChunk)
	}
}
