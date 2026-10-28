package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/writescheduler"
)

// The victim read seeks the partial expiry index in order, and the delete
// seeks each listed span by rowid; neither walks the spans table.
func TestBoringExpiryPlansSeekIndexes(t *testing.T) {
	repo := setupTestDB(t)
	now := time.Now().UTC()
	victims := queryPlan(t, repo, boringVictimsSQL, now, boringExpiryChunk)
	if !strings.Contains(victims, "SEARCH spans USING COVERING INDEX idx_spans_expires") &&
		!strings.Contains(victims, "SEARCH spans USING INDEX idx_spans_expires") ||
		strings.Contains(victims, "TEMP B-TREE") {
		t.Errorf("victim read does not seek idx_spans_expires in order:\n%s", victims)
	}
	del := queryPlan(t, repo, deleteBoringRowsSQL, "[1,2]", now)
	if !strings.Contains(del, "SEARCH spans USING INTEGER PRIMARY KEY (rowid=?)") || strings.Contains(del, "SCAN spans") {
		t.Errorf("delete does not seek spans by rowid:\n%s", del)
	}
}

// The delete checks each listed span again: a span whose expires_at moved past
// the cutoff after the chunk was read stays.
func TestDeleteBoringRowsChecksEachRowAgain(t *testing.T) {
	repo := setupTestDB(t)
	insertBoringSpans(t, repo, 1, 3, time.Now().Add(-2*time.Hour).UTC())
	now := time.Now().UTC()
	ids, keys, err := repo.boringVictims(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || len(keys) != 3 {
		t.Fatalf("read %d spans of %d traces, want 3 of 3", len(ids), len(keys))
	}
	later := now.Add(time.Hour)
	db := repo.writer(FamilySpans)
	if _, err := db.Exec(`UPDATE spans SET expires_at = ? WHERE rowid = ?`, later, ids[0]); err != nil {
		t.Fatal(err)
	}
	n, err := deleteRowsByID(context.Background(), db, deleteBoringRowsSQL, ids, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || countSpans(t, repo) != 1 {
		t.Errorf("deleted %d spans, %d left; want 2 deleted, 1 left", n, countSpans(t, repo))
	}
}

// With the chunk read on the read pool, expiry still deletes a trace's summary
// with its last span and refreshes the summary of a trace that keeps spans.
func TestDeleteExpiredBoringSpansWithReadPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spanbarn.db")
	repo := openTestStorage(t, path).Repository()
	ro, err := OpenReadDB(path, 2, 0)
	if err != nil {
		t.Fatalf("open read db: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	repo.SetLogTrimReads(ro.DB)

	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	expiring := func(s Span) Span { s.ExpiresAt = &past; return s }
	if err := repo.InsertSpans([]Span{
		expiring(tsSpan("mixed", "m1", "", "POST /contact", "web", "ok", 1, 10)),
		tsSpan("mixed", "m2", "m1", "send", "mail", "error", 2, 5),
		expiring(tsSpan("gone", "g1", "", "GET /", "web", "ok", 1, 10)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.writer(FamilySpans).Exec(`UPDATE trace_summaries SET expires_at = NULL WHERE trace_id = 'gone'`); err != nil {
		t.Fatal(err)
	}

	n, err := repo.DeleteExpiredBoringSpans(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted %d spans, want 2", n)
	}
	if _, ok := summaryIDs(t, repo)["gone"]; ok {
		t.Error("summary of a trace with no spans left is still listed")
	}
	hr, orphans, spans, root := summaryRow(t, repo, "mixed")
	if hr == nil || *hr || orphans != 1 || spans != 1 || root != "" {
		t.Errorf("mixed after expiry: hasRoot=%v orphans=%d spans=%d root=%q", hr, orphans, spans, root)
	}
}

// The span delete keeps the label the batched delete had, and the summary job
// has its own, so the two read apart in slow-write history.
func TestDeleteExpiredBoringSpansLabels(t *testing.T) {
	repo := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := writescheduler.New()
	repo.SetWriteScheduler(s)
	go s.Run(ctx)
	labelScheduler.Store(s)
	t.Cleanup(func() { labelScheduler.Store(nil) })

	insertBoringSpans(t, repo, 1, 1, time.Now().Add(-2*time.Hour).UTC())
	db := repo.writer(FamilySpans)
	for _, q := range []string{
		`CREATE TABLE label_probe (tbl TEXT, label TEXT)`,
		`CREATE TRIGGER spans_label AFTER DELETE ON spans
			BEGIN INSERT INTO label_probe VALUES ('spans', write_label()); END`,
		`CREATE TRIGGER summaries_label AFTER DELETE ON trace_summaries
			BEGIN INSERT INTO label_probe VALUES ('trace_summaries', write_label()); END`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE trace_summaries SET expires_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteExpiredBoringSpans(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rows, err := db.Query(`SELECT tbl, label FROM label_probe`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tbl, label string
		if err := rows.Scan(&tbl, &label); err != nil {
			t.Fatal(err)
		}
		got[tbl] = label
	}
	if got["spans"] != boringExpiryLabel || got["trace_summaries"] != boringSummaryLabel {
		t.Errorf("labels = %v, want spans %q and trace_summaries %q", got, boringExpiryLabel, boringSummaryLabel)
	}
}
