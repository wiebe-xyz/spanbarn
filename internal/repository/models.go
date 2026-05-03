package repository

import (
	"database/sql"
	"time"
)

type Project struct {
	ID        int64
	Slug      string
	Name      string
	CreatedAt time.Time
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

type APIKey struct {
	ID         int64
	ProjectID  int64
	Name       string
	KeyHash    string
	Scope      string
	LastUsedAt sql.NullTime
	CreatedAt  time.Time
}

type Span struct {
	ID           int64
	ProjectID    int64
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Service      string
	Resource     string
	Kind         string
	Status       string
	StartTimeUs  int64
	DurationUs   int64
	Attributes   string
	Events       string
	IngestedAt   time.Time
}

type Aggregate struct {
	ID            int64
	ProjectID     int64
	Service       string
	Operation     string
	Resource      string
	Kind          string
	Bucket        time.Time
	Count         int64
	ErrorCount    int64
	P50Us         int64
	P95Us         int64
	P99Us         int64
	MaxUs         int64
	SumDurationUs int64
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

type Alert struct {
	ID               int64
	ProjectID        int64
	Service          string
	Operation        string
	Type             string
	Threshold        float64
	ComparisonWindow int
	CooldownMinutes  int
	WebhookURL       string
	Email            string
	Enabled          bool
	LastTriggeredAt  sql.NullTime
	CreatedAt        time.Time
}
