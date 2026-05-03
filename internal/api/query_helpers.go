package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// parseTimeRange extracts from/to query params as time.Time values.
// Supports ISO 8601 (RFC 3339) and Unix timestamps (seconds).
func parseTimeRange(r *http.Request) (from, to time.Time, err error) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr != "" {
		from, err = parseTimeParam(fromStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	if toStr != "" {
		to, err = parseTimeParam(toStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	return from, to, nil
}

// parseTimeParam parses a single time parameter as either RFC 3339 or Unix seconds.
func parseTimeParam(s string) (time.Time, error) {
	// Try RFC 3339 first.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Try Unix timestamp.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(n, 0), nil
}

// parseIntParam extracts an integer query parameter with a default value.
func parseIntParam(r *http.Request, name string, defaultVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return n
}

// parseInt64Param extracts an int64 query parameter with a default value.
func parseInt64Param(r *http.Request, name string, defaultVal int64) int64 {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return defaultVal
	}
	return n
}

// pathParam extracts a path segment by name from a URL path.
// For a pattern like /api/v1/services/{service}/operations/{operation}/timeseries,
// this function extracts values between known path segments.
func pathParam(path, name string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Extract by position based on known route patterns:
	// /api/v1/services/{service}/operations/{operation}/timeseries
	// 0   1   2        3         4           5          6
	// /api/v1/traces/{traceId}
	// 0   1   2      3
	switch name {
	case "service":
		if len(parts) > 3 {
			return parts[3]
		}
	case "operation":
		if len(parts) > 5 {
			return parts[5]
		}
	case "traceId":
		if len(parts) > 3 {
			return parts[3]
		}
	}
	return ""
}

// parseInterval parses an interval string like "1m", "5m", "15m", "1h".
func parseInterval(s string) time.Duration {
	switch s {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	default:
		return time.Minute // default 1m
	}
}
