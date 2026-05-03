package spanbarn

import (
	"context"
	"sync"
	"time"
)

// Config configures the SpanBarn SDK.
type Config struct {
	Endpoint      string
	APIKey        string
	Service       string
	Environment   string
	FlushInterval time.Duration               // default 5s
	MaxBatchSize  int                         // default 100
	MaxQueueSize  int                         // default 1000
	Debug         bool
	Disabled      bool
	BeforeSend    func(*SpanData) *SpanData   // filter/modify spans before sending
}

var (
	defaultMu     sync.Mutex
	defaultClient *Client
)

// Init initializes the global SpanBarn client. Safe to call multiple times.
func Init(cfg Config) *Client {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClient != nil {
		_ = defaultClient.Shutdown()
	}
	defaultClient = NewClient(cfg)
	return defaultClient
}

// Shutdown shuts down the global client and flushes remaining spans.
func Shutdown() error {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultClient == nil {
		return nil
	}
	err := defaultClient.Shutdown()
	defaultClient = nil
	return err
}

// Start creates a span using the global client.
func Start(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	defaultMu.Lock()
	c := defaultClient
	defaultMu.Unlock()
	if c == nil {
		// Return a no-op span that won't enqueue anything
		spanID := generateSpanID()
		traceID := generateTraceID()
		return ctx, &Span{
			data: SpanData{
				TraceID:   traceID,
				SpanID:    spanID,
				Name:      name,
				Kind:      "internal",
				Status:    "ok",
				StartTime: time.Now().UnixMicro(),
			},
		}
	}
	return c.Start(ctx, name, opts...)
}

// GetDefaultClient returns the current global client, or nil if not initialized.
func GetDefaultClient() *Client {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultClient
}
