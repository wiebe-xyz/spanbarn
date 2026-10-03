package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
)

// AttributeSetWindow bounds the value-distribution scan of one span set. Expr
// selects the set and may be nil for every span of the project in the range.
type AttributeSetWindow struct {
	ProjectID int64
	From, To  time.Time
	Expr      *filter.Expr
	// Sample keeps spans with id % Sample = 0 (1 reads every span) and MaxSpans
	// caps how many spans are read, newest first.
	Sample   int
	MaxSpans int
	// ValueCap is how many distinct values are kept per key. The rest of a key's
	// values are summed into AttributeSetValues.Other.
	ValueCap int

	// calc resolves the project's calculated fields in Expr. ScanAttributeSet
	// sets it.
	calc filter.Resolver
}

// AttributeSetValues is the value distribution of one key over a span set.
type AttributeSetValues struct {
	// Counts maps a value to the number of scanned spans that carry it.
	Counts map[string]int64
	// Other is the number of spans whose value fell outside ValueCap.
	Other int64
}

// AttributeSetScan is the distribution of every key over one span set.
type AttributeSetScan struct {
	// Scanned is the number of spans read.
	Scanned int64
	Keys    map[string]*AttributeSetValues
}

func attributeSetCTE(w AttributeSetWindow) (string, []any, error) {
	if w.From.IsZero() {
		return "", nil, fmt.Errorf("%w: a time range (from) is required", filter.ErrInvalid)
	}
	pred, predArgs, err := filter.CompileWith(w.Expr, w.calc)
	if err != nil {
		return "", nil, err
	}
	q := `s AS (
		SELECT attributes, service, name, kind, status FROM spans
		WHERE project_id = ? AND ingested_at >= ? AND ingested_at <= ?`
	args := []any{w.ProjectID, w.From, w.To}
	if pred != "" {
		q += ` AND (` + pred + `)`
		args = append(args, predArgs...)
	}
	if w.Sample > 1 {
		q += ` AND id % ? = 0`
		args = append(args, w.Sample)
	}
	q += ` AND json_valid(attributes)
		ORDER BY ingested_at DESC
		LIMIT ?
	)`
	return q, append(args, w.MaxSpans), nil
}

// attributeSetValuesSQL groups the values of every key of the spans in `s`. The
// span columns service, name, kind and status are reported next to the
// attributes (is_attr = 0) so a comparison can surface them too.
const attributeSetValuesSQL = `
	kv AS (
		SELECT j.key AS k, CAST(j.value AS TEXT) AS v, COUNT(*) AS c, 1 AS is_attr
		FROM s, json_each(s.attributes) j
		WHERE j.type NOT IN ('null', 'object', 'array')
		GROUP BY k, v
		UNION ALL SELECT 'service', service, COUNT(*), 0 FROM s GROUP BY service
		UNION ALL SELECT 'name', name, COUNT(*), 0 FROM s GROUP BY name
		UNION ALL SELECT 'kind', kind, COUNT(*), 0 FROM s GROUP BY kind
		UNION ALL SELECT 'status', status, COUNT(*), 0 FROM s GROUP BY status
	),
	ranked AS (
		SELECT k, v, c, is_attr,
		       ROW_NUMBER() OVER (PARTITION BY k, is_attr ORDER BY c DESC, v) AS rn
		FROM kv
	)
	SELECT k, is_attr, CASE WHEN rn <= ? THEN v END AS v, SUM(c)
	FROM ranked
	GROUP BY k, is_attr, CASE WHEN rn <= ? THEN v END`

// ScanAttributeSet reads the value distribution of every attribute key, and of
// the span columns service, name, kind and status, over the spans of one set.
// Per key it keeps the ValueCap most common values and sums the remainder, so
// memory stays bounded by keys times ValueCap however many distinct values
// (trace ids, urls) the set holds.
func (r *Repository) ScanAttributeSet(ctx context.Context, w AttributeSetWindow) (*AttributeSetScan, error) {
	ctx, cancel := context.WithTimeout(ctx, r.queryTimeout)
	defer cancel()

	var err error
	if w.Expr != nil && len(w.Expr.Filters) > 0 {
		if w.calc, err = r.calcResolver(w.ProjectID); err != nil {
			return nil, err
		}
	}
	cte, args, err := attributeSetCTE(w)
	if err != nil {
		return nil, err
	}
	scan := &AttributeSetScan{Keys: map[string]*AttributeSetValues{}}
	if err := r.db.QueryRowContext(ctx, `WITH `+cte+` SELECT COUNT(*) FROM s`, args...).Scan(&scan.Scanned); err != nil {
		return nil, err
	}
	if scan.Scanned == 0 {
		return scan, nil
	}

	rows, err := r.db.QueryContext(ctx, `WITH `+cte+`,`+attributeSetValuesSQL, append(args, w.ValueCap, w.ValueCap)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			key    string
			isAttr bool
			val    sql.NullString
			count  int64
		)
		if err := rows.Scan(&key, &isAttr, &val, &count); err != nil {
			return nil, err
		}
		if isAttr {
			key = filter.AttrKey(key)
		}
		sv := scan.Keys[key]
		if sv == nil {
			sv = &AttributeSetValues{Counts: map[string]int64{}}
			scan.Keys[key] = sv
		}
		if !val.Valid {
			sv.Other += count
			continue
		}
		sv.Counts[val.String] += count
	}
	return scan, rows.Err()
}
