package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"sort"
)

// operationRow is one operation of a service as the operations endpoint reports
// it, plus the optional per-unit ratio.
type operationRow struct {
	Operation  string   `json:"operation"`
	Resource   string   `json:"resource,omitempty"`
	Kind       string   `json:"kind"`
	SpanCount  int64    `json:"spanCount"`
	ErrorCount int64    `json:"errorCount"`
	ErrorRate  float64  `json:"errorRate"`
	P50Us      int64    `json:"p50Us"`
	P95Us      int64    `json:"p95Us"`
	P99Us      int64    `json:"p99Us"`
	PerUnit    *float64 `json:"perUnit,omitempty"`
}

// withPerUnit sets PerUnit on every row to its span count divided by the span
// count of the operation named per, and orders the rows by that ratio, highest
// first. It fails when the operation is absent or has no spans, since every ratio
// would be meaningless.
func withPerUnit(rows []operationRow, per string) ([]operationRow, error) {
	var base int64
	for _, r := range rows {
		if r.Operation == per {
			base += r.SpanCount
		}
	}
	if base == 0 {
		return nil, fmt.Errorf("operation %q has no spans in this window", per)
	}
	out := make([]operationRow, len(rows))
	copy(out, rows)
	for i := range out {
		v := float64(out[i].SpanCount) / float64(base)
		out[i].PerUnit = &v
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].PerUnit > *out[j].PerUnit })
	return out, nil
}

// cmdOperations lists the operations of one service with their span counts and
// latency. With --per OP it adds how many spans each operation has per span of
// OP, which answers "client calls per page view" in one call.
func cmdOperations(args []string) error {
	fs := flag.NewFlagSet("operations", flag.ContinueOnError)
	project := commonFlags(fs)
	from, to := addTimeFlags(fs)
	service := fs.String("service", "", "service name (required)")
	kind := fs.String("kind", "", "only spans of this kind (server, client, internal, ...)")
	per := fs.String("per", "", "add perUnit: span count divided by the span count of this operation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *service == "" {
		return errors.New("--service is required")
	}
	client, err := scopedClient(*project)
	if err != nil {
		return err
	}
	params := url.Values{}
	applyTimeRange(params, *from, *to)
	if *kind != "" {
		params.Set("kind", *kind)
	}
	data, err := client.query("/api/v1/services/"+url.PathEscape(*service)+"/operations", params)
	if err != nil {
		return err
	}
	if *per == "" {
		return emit(data)
	}
	var rows []operationRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return fmt.Errorf("decode operations: %w", err)
	}
	rows, err = withPerUnit(rows, *per)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return emit(raw)
}
