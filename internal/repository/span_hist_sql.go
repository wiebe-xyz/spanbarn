package repository

import (
	"database/sql/driver"

	"modernc.org/sqlite"

	"github.com/wiebe-xyz/spanbarn/internal/spanhist"
)

// spanHistMergeFunc is the SQL function that merges two encoded span
// histograms. It is registered for every connection the driver opens.
const spanHistMergeFunc = "span_hist_merge"

func init() {
	if err := sqlite.RegisterDeterministicScalarFunction(spanHistMergeFunc, 2, spanHistMerge); err != nil {
		panic(err)
	}
}

// spanHistMerge returns the merge of two encoded histograms. A side that does
// not decode is dropped and the other side is kept, so one damaged row cannot
// fail every later flush of its key.
func spanHistMerge(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	a, _ := args[0].([]byte)
	b, _ := args[1].([]byte)
	merged, err := spanhist.MergeEncoded(a, b)
	if err == nil {
		return merged, nil
	}
	if _, errA := spanhist.Decode(a); errA != nil {
		return b, nil
	}
	return a, nil
}
