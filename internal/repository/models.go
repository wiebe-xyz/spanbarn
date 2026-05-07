package repository

import (
	"database/sql"
	"time"
)

type Project struct {
	ID        int64     `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

type APIKey struct {
	ID         int64        `json:"id"`
	ProjectID  int64        `json:"projectId"`
	Name       string       `json:"name"`
	KeyHash    string       `json:"-"`
	Scope      string       `json:"scope"`
	LastUsedAt sql.NullTime `json:"lastUsedAt"`
	CreatedAt  time.Time    `json:"createdAt"`
}

type Span struct {
	ID           int64     `json:"id"`
	ProjectID    int64     `json:"projectId"`
	TraceID      string    `json:"traceId"`
	SpanID       string    `json:"spanId"`
	ParentSpanID string    `json:"parentSpanId"`
	Name         string    `json:"name"`
	Service      string    `json:"service"`
	Resource     string    `json:"resource"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	StartTimeUs  int64     `json:"startTimeUs"`
	DurationUs   int64     `json:"durationUs"`
	Attributes   string    `json:"attributes"`
	Events       string    `json:"events"`
	IngestedAt   time.Time `json:"ingestedAt"`
}

type Aggregate struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"projectId"`
	Service       string    `json:"service"`
	Operation     string    `json:"operation"`
	Resource      string    `json:"resource"`
	Kind          string    `json:"kind"`
	Bucket        time.Time `json:"bucket"`
	Count         int64     `json:"count"`
	ErrorCount    int64     `json:"errorCount"`
	P50Us         int64     `json:"p50Us"`
	P95Us         int64     `json:"p95Us"`
	P99Us         int64     `json:"p99Us"`
	MaxUs         int64     `json:"maxUs"`
	SumDurationUs int64     `json:"sumDurationUs"`
}

type SpanFilter struct {
	ProjectID   int64
	Service     string
	Operation   string
	Status      string
	MinDuration int64
	From        time.Time
	To          time.Time
	Limit       int
	Offset      int
}

type AggregateFilter struct {
	ProjectID int64
	Service   string
	Operation string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

type PromptRecord struct {
	ID                 int64        `json:"id"`
	ProjectID          int64        `json:"projectId"`
	TraceID            string       `json:"traceId"`
	SpanID             string       `json:"spanId"`
	ParentSpanID       string       `json:"parentSpanId"`
	Service            string       `json:"service"`
	Name               string       `json:"name"`
	GenAISystem        string       `json:"genAiSystem"`
	Model              string       `json:"model"`
	Temperature        *float64     `json:"temperature"`
	MaxTokens          *int64       `json:"maxTokens"`
	PromptBody         string       `json:"promptBody"`
	ResponseBody       string       `json:"responseBody"`
	InputTokens           int64    `json:"inputTokens"`
	OutputTokens          int64    `json:"outputTokens"`
	TotalTokens           int64    `json:"totalTokens"`
	CachedInputTokens     int64    `json:"cachedInputTokens"`
	ReasoningOutputTokens int64    `json:"reasoningOutputTokens"`
	CostUSD               float64  `json:"costUsd"`
	InputCostUSD          float64  `json:"inputCostUsd"`
	OutputCostUSD         float64  `json:"outputCostUsd"`
	DurationUs         int64        `json:"durationUs"`
	Status             string       `json:"status"`
	FinishReason       string       `json:"finishReason"`
	PromptTemplate     string       `json:"promptTemplate"`
	PromptHash         string       `json:"promptHash"`
	Outcome            string       `json:"outcome"`
	QualityScore       *float64     `json:"qualityScore"`
	FeatureFlagKey     string       `json:"featureFlagKey"`
	FeatureFlagVariant string       `json:"featureFlagVariant"`
	StartTimeUs        int64        `json:"startTimeUs"`
	IngestedAt         time.Time    `json:"ingestedAt"`
}

type PromptFilter struct {
	ProjectID    int64
	Service      string
	Model        string
	GenAISystem  string
	Status       string
	FinishReason string
	PromptHash   string
	MinDuration  int64
	From         time.Time
	To           time.Time
	Limit        int
	Offset       int
}

type Alert struct {
	ID               int64        `json:"id"`
	ProjectID        int64        `json:"projectId"`
	Service          string       `json:"service"`
	Operation        string       `json:"operation"`
	Type             string       `json:"type"`
	Threshold        float64      `json:"threshold"`
	ComparisonWindow int          `json:"comparisonWindow"`
	CooldownMinutes  int          `json:"cooldownMinutes"`
	WebhookURL       string       `json:"webhookUrl"`
	Email            string       `json:"email"`
	Enabled          bool         `json:"enabled"`
	LastTriggeredAt  sql.NullTime `json:"lastTriggeredAt"`
	CreatedAt        time.Time    `json:"createdAt"`
}
