package spanbarn

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type contextKey struct{}

type spanContext struct {
	TraceID string
	SpanID  string
}

// withSpanContext stores a spanContext in the given context.
func withSpanContext(ctx context.Context, sc spanContext) context.Context {
	return context.WithValue(ctx, contextKey{}, sc)
}

// spanContextFromContext retrieves a spanContext from the given context.
func spanContextFromContext(ctx context.Context) (spanContext, bool) {
	sc, ok := ctx.Value(contextKey{}).(spanContext)
	return sc, ok
}

// MakeTraceparent formats a W3C traceparent header value.
func MakeTraceparent(traceID, spanID string) string {
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID)
}

// ParseTraceparent parses a W3C traceparent header value.
// Returns the traceID, spanID, and whether parsing succeeded.
func ParseTraceparent(header string) (traceID, spanID string, ok bool) {
	parts := strings.Split(header, "-")
	if len(parts) != 4 {
		return "", "", false
	}
	if parts[0] != "00" {
		return "", "", false
	}
	if len(parts[1]) != 32 || len(parts[2]) != 16 {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// InjectTraceparent writes the traceparent header into the given HTTP headers.
func InjectTraceparent(header http.Header, traceID, spanID string) {
	header.Set("Traceparent", MakeTraceparent(traceID, spanID))
}

// ExtractTraceparent reads and parses the traceparent header from the given HTTP headers.
func ExtractTraceparent(header http.Header) (traceID, spanID string, ok bool) {
	tp := header.Get("Traceparent")
	if tp == "" {
		return "", "", false
	}
	return ParseTraceparent(tp)
}
