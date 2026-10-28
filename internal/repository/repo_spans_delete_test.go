package repository

import (
	"errors"
	"fmt"
	"testing"
)

// The chunked delete removes exactly the spans with id <= maxID, as the
// single DELETE it replaces did, across more than one chunk.
func TestDeleteSpansByMaxIDDeletesInChunks(t *testing.T) {
	repo := setupTestDB(t)
	const total, upTo = 2*spanDeleteChunk + 500, 2*spanDeleteChunk + 100
	spans := make([]Span, total)
	for i := range spans {
		spans[i] = tsSpan(fmt.Sprintf("t%d", i/5), fmt.Sprintf("s%d", i), "", "op", "web", "ok", 1, 10)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	var maxID, want int64
	if err := repo.DB().QueryRow(`SELECT id FROM spans ORDER BY id LIMIT 1 OFFSET ?`, upTo-1).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DB().QueryRow(`SELECT count(*) FROM spans WHERE id <= ?`, maxID).Scan(&want); err != nil {
		t.Fatal(err)
	}

	n, err := repo.DeleteSpansByMaxID(maxID)
	if err != nil {
		t.Fatal(err)
	}
	if n != want || n != upTo {
		t.Errorf("deleted %d spans, want %d", n, want)
	}
	var left, below int64
	if err := repo.DB().QueryRow(`SELECT count(*), count(CASE WHEN id <= ? THEN 1 END) FROM spans`, maxID).Scan(&left, &below); err != nil {
		t.Fatal(err)
	}
	if left != total-upTo || below != 0 {
		t.Errorf("left %d spans, %d of them at or below maxID; want %d and 0", left, below, total-upTo)
	}
}

// The loop ends on the first chunk that deletes fewer rows than a chunk holds,
// a chunk that deletes nothing included, and on the first error.
func TestDeleteInChunksStops(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name      string
		chunks    []int64
		err       error
		wantCalls int
		wantTotal int64
	}{
		{"no rows", []int64{0}, nil, 1, 0},
		{"exact multiple ends on an empty chunk", []int64{10, 10, 0}, nil, 3, 20},
		{"short chunk", []int64{10, 4}, nil, 2, 14},
		{"error", []int64{10, 3}, boom, 2, 13},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			total, err := deleteInChunks(10, func() (int64, error) {
				if calls >= len(c.chunks) {
					t.Fatalf("chunk called %d times, want %d", calls+1, c.wantCalls)
				}
				n := c.chunks[calls]
				calls++
				if calls == len(c.chunks) {
					return n, c.err
				}
				return n, nil
			})
			if calls != c.wantCalls || total != c.wantTotal || !errors.Is(err, c.err) {
				t.Errorf("calls %d total %d err %v; want %d, %d, %v", calls, total, err, c.wantCalls, c.wantTotal, c.err)
			}
		})
	}
}
