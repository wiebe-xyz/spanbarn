package model

import "encoding/json"

// MetricType is the OTLP metric data kind.
type MetricType string

const (
	MetricTypeGauge                MetricType = "gauge"
	MetricTypeSum                  MetricType = "sum"
	MetricTypeHistogram            MetricType = "histogram"
	MetricTypeExponentialHistogram MetricType = "exp_histogram"
	MetricTypeSummary              MetricType = "summary"
)

// MetricRecord is the in-process representation of a single OTLP data point.
// One MetricRecord maps to one row in the metrics table.
type MetricRecord struct {
	ProjectID         int64
	Name              string
	Description       string
	Unit              string
	Type              MetricType
	TimeUnixNano      uint64
	StartTimeUnixNano uint64
	// Value is the scalar for gauge/sum, or the sum-of-observations for histogram/summary.
	Value      float64
	Count      uint64          // histogram / summary count
	Attributes json.RawMessage // merged resource < scope < data-point attributes
	// Extra holds type-specific payload as JSON:
	//   histogram     — {"bounds":[…],"counts":[…]}
	//   exp_histogram — {"scale":N,"zero_count":N,"positive":{…},"negative":{…}}
	//   summary       — {"quantiles":[{"quantile":0.5,"value":1.23},…]}
	Extra json.RawMessage
}
