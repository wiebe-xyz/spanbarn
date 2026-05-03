package aggregation

import (
	"fmt"
	"time"
)

// TruncateToBucket truncates t down to the nearest multiple of interval.
func TruncateToBucket(t time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return t
	}
	return t.Truncate(interval)
}

// ParseInterval converts a human-readable interval string to a time.Duration.
// Supported values: "1m", "5m", "15m", "1h".
func ParseInterval(s string) (time.Duration, error) {
	switch s {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported interval %q: valid values are 1m, 5m, 15m, 1h", s)
	}
}
