package service

import (
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

// ServiceSummary holds aggregated metrics for a single service.
type ServiceSummary struct {
	Service    string  `json:"service"`
	SpanCount  int64   `json:"spanCount"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	P50Us      int64   `json:"p50Us"`
	P95Us      int64   `json:"p95Us"`
	P99Us      int64   `json:"p99Us"`
}

// OperationSummary holds aggregated metrics for a single operation.
type OperationSummary struct {
	Operation  string  `json:"operation"`
	Resource   string  `json:"resource"`
	Kind       string  `json:"kind"`
	SpanCount  int64   `json:"spanCount"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	P50Us      int64   `json:"p50Us"`
	P95Us      int64   `json:"p95Us"`
	P99Us      int64   `json:"p99Us"`
}

// TimeseriesBucket holds metrics for one time bucket.
type TimeseriesBucket struct {
	Bucket     time.Time `json:"bucket"`
	Count      int64     `json:"count"`
	ErrorCount int64     `json:"errorCount"`
	P50Us      int64     `json:"p50Us"`
	P95Us      int64     `json:"p95Us"`
	P99Us      int64     `json:"p99Us"`
}

// TraceSummary holds a summary of a single trace for listing.
type TraceSummary struct {
	TraceID      string    `json:"traceId"`
	RootSpanName string    `json:"rootSpanName"`
	RootService  string    `json:"rootService"`
	DurationUs   int64     `json:"durationUs"`
	SpanCount    int       `json:"spanCount"`
	Status       string    `json:"status"`
	StartTime    time.Time `json:"startTime"`
}

// TraceDetail holds a full trace with all its spans.
type TraceDetail struct {
	TraceID    string            `json:"traceId"`
	Spans      []repository.Span `json:"spans"`
	DurationUs int64             `json:"durationUs"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
}

// DependencySummary holds metrics for a single dependency target.
type DependencySummary struct {
	Target     string  `json:"target"`
	TargetType string  `json:"targetType"`
	CallCount  int64   `json:"callCount"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	P50Us      int64   `json:"p50Us"`
	P95Us      int64   `json:"p95Us"`
	P99Us      int64   `json:"p99Us"`
}

// TraceSearchFilter holds parameters for searching traces.
type TraceSearchFilter struct {
	ProjectID     int64
	Service       string
	Operation     string
	Status        string
	MinDurationUs int64
	From          time.Time
	To            time.Time
	Limit         int
	Offset        int
}
