package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// spansSampleWeightsUp adds the sample weight columns (issue #333).
//
// sample_weight on spans_staging and spans is how many traces the span's trace
// stands for, as the ingest trace buffer decided when it kept the trace.
// weighted_count and weighted_spans on aggregates sum those weights and count
// the spans that had one, so a query can tell a bucket whose every span was
// weighted from one that holds spans of unknown weight.
//
// All four are nullable without a default: ADD COLUMN then only rewrites the
// schema, existing rows read NULL, and an older binary that does not name the
// columns keeps working. The aggregates upsert adds into weighted_count, so a
// NULL stays NULL and that bucket keeps using the ratio estimate.
func spansSampleWeightsUp(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{
		`ALTER TABLE spans_staging ADD COLUMN sample_weight INTEGER`,
		`ALTER TABLE spans ADD COLUMN sample_weight INTEGER`,
		`ALTER TABLE aggregates ADD COLUMN weighted_count INTEGER`,
		`ALTER TABLE aggregates ADD COLUMN weighted_spans INTEGER`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sample weights: %w", err)
		}
	}
	return nil
}
