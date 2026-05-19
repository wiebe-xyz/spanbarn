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
//
// Spans is capped at MaxTraceDetailSpans; when a trace exceeds the cap the
// response includes only the first MaxTraceDetailSpans (by start_time_us)
// and sets Truncated=true with TotalSpans set to the real count so the UI
// can show "showing N of M spans".
type TraceDetail struct {
	TraceID    string            `json:"traceId"`
	Spans      []repository.Span `json:"spans"`
	DurationUs int64             `json:"durationUs"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	TotalSpans int               `json:"totalSpans"`
	Truncated  bool              `json:"truncated,omitempty"`
}

// MaxTraceDetailSpans bounds a single trace-detail response. Picked because
// past this point the UI's flat list becomes the bottleneck, not the API,
// and the JSON payload grows into the multi-MB range.
const MaxTraceDetailSpans = 2000

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

// ServiceMapNode holds a service in the service map.
type ServiceMapNode struct {
	ID         string  `json:"id"`
	SpanCount  int64   `json:"spanCount"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
}

// ServiceMapEdge holds a connection between two services.
type ServiceMapEdge struct {
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	TargetType string  `json:"targetType"`
	CallCount  int64   `json:"callCount"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
}

// ServiceMap holds the full service topology.
type ServiceMap struct {
	Nodes []ServiceMapNode `json:"nodes"`
	Edges []ServiceMapEdge `json:"edges"`
}

// DatabaseQuerySpan is a single span execution of a database query,
// enriched with caller context from the parent span.
type DatabaseQuerySpan struct {
	SpanID        string `json:"spanId"`
	TraceID       string `json:"traceId"`
	ParentSpanID  string `json:"parentSpanId"`
	Service       string `json:"service"`
	CallerName    string `json:"callerName"`    // parent span's operation name
	CallerService string `json:"callerService"` // parent span's service
	DurationUs    int64  `json:"durationUs"`
	Status        string `json:"status"`
	ErrorMessage  string `json:"errorMessage"`
	StartTimeUs   int64  `json:"startTimeUs"`
	IngestedAt    string `json:"ingestedAt"`
}

// DatabaseQuerySummary holds metrics for a single normalized query pattern.
type DatabaseQuerySummary struct {
	Pattern     string  `json:"pattern"`
	Operation   string  `json:"operation"`
	DBSystem    string  `json:"dbSystem"`
	DBName      string  `json:"dbName"`
	CallCount   int64   `json:"callCount"`
	ErrorCount  int64   `json:"errorCount"`
	ErrorRate   float64 `json:"errorRate"`
	P50Us       int64   `json:"p50Us"`
	P95Us       int64   `json:"p95Us"`
	P99Us       int64   `json:"p99Us"`
	TotalTimeUs int64   `json:"totalTimeUs"`
}

// PromptSummary holds aggregated metrics for a single prompt operation.
type PromptSummary struct {
	Name         string  `json:"name"`
	GenAISystem  string  `json:"genAiSystem"`
	Model        string  `json:"model"`
	Service      string  `json:"service"`
	CallCount    int64   `json:"callCount"`
	ErrorCount   int64   `json:"errorCount"`
	ErrorRate    float64 `json:"errorRate"`
	P50Us        int64   `json:"p50Us"`
	P95Us        int64   `json:"p95Us"`
	P99Us        int64   `json:"p99Us"`
	TotalTimeUs  int64   `json:"totalTimeUs"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	TotalCostUSD float64 `json:"totalCostUsd"`
}

// TraceSearchFilter holds parameters for searching traces.
type TraceSearchFilter struct {
	ProjectID         int64
	Service           string
	Operation         string
	Status            string
	MinDurationUs     int64
	MinSpans          int
	RootOnly          bool
	SortErrorsFirst   bool
	ExcludeOperations []string
	From              time.Time
	To                time.Time
	Limit             int
	Offset            int
}

// TraceGroupSummary holds aggregated metrics for a group of traces sharing the same root operation.
type TraceGroupSummary struct {
	Operation  string  `json:"operation"`
	Service    string  `json:"service"`
	Count      int64   `json:"count"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	P50Us      int64   `json:"p50Us"`
	P95Us      int64   `json:"p95Us"`
	P99Us      int64   `json:"p99Us"`
}
