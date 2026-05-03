package spanbarn

import (
	"crypto/rand"
	"encoding/hex"
)

// generateTraceID returns a 32 hex-character trace ID using crypto/rand.
func generateTraceID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// generateSpanID returns a 16 hex-character span ID using crypto/rand.
func generateSpanID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
