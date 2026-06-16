package model

import "encoding/json"

// LogRecord is the in-memory representation of a single OTLP log data point.
type LogRecord struct {
	ProjectID            int64
	TraceID              string // empty if not correlated with a trace
	SpanID               string // empty if not tied to a specific span
	SeverityNumber       int32  // OTLP SeverityNumber (0=unset, 9=INFO, 13=WARN, 17=ERROR)
	SeverityText         string // human label ("INFO", "WARN", "ERROR", …)
	TimeUnixNano         uint64
	ObservedTimeUnixNano uint64
	Body                 string          // stringified; StringValue direct, KvlistValue as JSON
	Attributes           json.RawMessage // merged: resource < scope < log-record attributes
}
