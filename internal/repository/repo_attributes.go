package repository

import (
	"context"
	"time"
)

// AttributeWindow bounds an attribute discovery scan. ProjectID, From and To are
// required by the service. Sample keeps spans with id % Sample = 0 (1 reads
// every span) and MaxSpans caps how many spans the scan reads, newest first.
type AttributeWindow struct {
	ProjectID int64
	From, To  time.Time
	SpanName  string
	Service   string
	// Key restricts the result to one attribute key (value autocomplete).
	Key      string
	Sample   int
	MaxSpans int
	// MaxKeys caps the keys returned, TopValues the values kept per key and
	// DistinctCap the cardinality reported for a key.
	MaxKeys     int
	TopValues   int
	DistinctCap int
}

// AttributeValueCount is one observed value of an attribute and its span count.
type AttributeValueCount struct {
	Value string
	Count int64
}

// AttributeKeyStats describes one attribute key over the scanned spans.
type AttributeKeyStats struct {
	Key string
	// Spans is how many scanned spans populate the key.
	Spans int64
	// Distinct is the number of distinct values, capped at DistinctCap.
	Distinct       int64
	DistinctCapped bool
	Top            []AttributeValueCount
}

// AttributeScan is the result of one attribute discovery scan.
type AttributeScan struct {
	// Scanned is the number of spans the scan read.
	Scanned int64
	Keys    []AttributeKeyStats
}

// attributeSpansCTE selects the spans a scan reads. The LIMIT keeps SQLite from
// flattening the CTE, so json_valid runs before json_each and a span with
// malformed attributes is skipped instead of failing the query.
func attributeSpansCTE(w AttributeWindow) (string, []any) {
	q := `s AS (
		SELECT attributes FROM spans
		WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?`
	args := []any{w.ProjectID, w.From, w.To}
	if w.SpanName != "" {
		q += ` AND name = ?`
		args = append(args, w.SpanName)
	}
	if w.Service != "" {
		q += ` AND service = ?`
		args = append(args, w.Service)
	}
	if w.Sample > 1 {
		q += ` AND id % ? = 0`
		args = append(args, w.Sample)
	}
	q += ` AND json_valid(attributes)
		ORDER BY ingested_at DESC
		LIMIT ?
	)`
	return q, append(args, w.MaxSpans)
}

// ScanAttributes reads the attributes JSON of the spans in the window with
// json_each and returns, per key, how many spans populate it, its distinct value
// count and its most common values. Storage decision: deploy/docs/attribute-storage-design.md.
// Spans store flat dotted keys, so json_each yields them as keys directly.
func (r *Repository) ScanAttributes(ctx context.Context, w AttributeWindow) (*AttributeScan, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	cte, args := attributeSpansCTE(w)
	scan := &AttributeScan{}
	if err := r.db.QueryRowContext(ctx, `WITH `+cte+` SELECT COUNT(*) FROM s`, args...).Scan(&scan.Scanned); err != nil {
		return nil, err
	}
	if scan.Scanned == 0 {
		return scan, nil
	}

	keyFilter := ""
	if w.Key != "" {
		keyFilter = ` AND j.key = ?`
		args = append(args, w.Key)
	}
	args = append(args, w.TopValues, w.MaxKeys)
	rows, err := r.db.QueryContext(ctx, `
		WITH `+cte+`,
		kv AS (
			SELECT j.key AS k, CAST(j.value AS TEXT) AS v, COUNT(*) AS c
			FROM s, json_each(s.attributes) j
			WHERE j.type NOT IN ('null', 'object', 'array')`+keyFilter+`
			GROUP BY k, v
		),
		ranked AS (
			SELECT k, v, c,
			       SUM(c) OVER (PARTITION BY k) AS total,
			       COUNT(*) OVER (PARTITION BY k) AS distinct_n,
			       ROW_NUMBER() OVER (PARTITION BY k ORDER BY c DESC, v) AS rn
			FROM kv
		),
		keyed AS (
			SELECT k, v, c, total, distinct_n, rn,
			       DENSE_RANK() OVER (ORDER BY total DESC, k) AS kr
			FROM ranked
		)
		SELECT k, v, c, total, distinct_n
		FROM keyed
		WHERE rn <= ? AND kr <= ?
		ORDER BY kr, rn`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key, val        string
			count, total, n int64
		)
		if err := rows.Scan(&key, &val, &count, &total, &n); err != nil {
			return nil, err
		}
		last := len(scan.Keys) - 1
		if last < 0 || scan.Keys[last].Key != key {
			ks := AttributeKeyStats{Key: key, Spans: total, Distinct: n}
			if int(n) > w.DistinctCap {
				ks.Distinct, ks.DistinctCapped = int64(w.DistinctCap), true
			}
			scan.Keys = append(scan.Keys, ks)
			last++
		}
		scan.Keys[last].Top = append(scan.Keys[last].Top, AttributeValueCount{Value: val, Count: count})
	}
	return scan, rows.Err()
}
