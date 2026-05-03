package spanbarn

import (
	"context"
	"sync"
	"time"
)

// Client is the SpanBarn tracing client that manages span creation and export.
type Client struct {
	cfg       Config
	queue     chan *SpanData
	transport *Transport
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewClient creates and starts a new SpanBarn client with the given config.
func NewClient(cfg Config) *Client {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}
	if cfg.MaxBatchSize <= 0 {
		cfg.MaxBatchSize = 100
	}
	if cfg.MaxQueueSize <= 0 {
		cfg.MaxQueueSize = 1000
	}

	c := &Client{
		cfg:       cfg,
		queue:     make(chan *SpanData, cfg.MaxQueueSize),
		transport: newTransport(cfg.Endpoint, cfg.APIKey),
		done:      make(chan struct{}),
	}

	if !cfg.Disabled {
		c.wg.Add(1)
		go c.flushLoop()
	}

	return c
}

// Start creates a new span with the given name and options.
// It returns a new context containing the span's trace context and the span itself.
func (c *Client) Start(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	traceID := ""
	parentSpanID := ""

	if sc, ok := spanContextFromContext(ctx); ok {
		traceID = sc.TraceID
		parentSpanID = sc.SpanID
	}
	if traceID == "" {
		traceID = generateTraceID()
	}
	spanID := generateSpanID()

	data := SpanData{
		TraceID:      traceID,
		SpanID:       spanID,
		ParentSpanID: parentSpanID,
		Name:         name,
		Service:      c.cfg.Service,
		Kind:         "internal",
		Status:       "ok",
		StartTime:    time.Now().UnixMicro(),
	}

	for _, opt := range opts {
		opt(&data)
	}

	span := &Span{
		client: c,
		data:   data,
	}

	if c.cfg.Disabled {
		span.client = nil // prevent enqueue on End
	}

	newCtx := withSpanContext(ctx, spanContext{
		TraceID: traceID,
		SpanID:  spanID,
	})

	return newCtx, span
}

// enqueue adds a completed span to the send queue. Drops silently if the queue is full.
func (c *Client) enqueue(span *SpanData) {
	if c.cfg.Disabled {
		return
	}
	if c.cfg.BeforeSend != nil {
		span = c.cfg.BeforeSend(span)
		if span == nil {
			return
		}
	}
	select {
	case c.queue <- span:
	default:
		// queue full — drop silently
		if c.cfg.Debug {
			// could log here
		}
	}
}

// flush drains the queue and sends all pending spans.
func (c *Client) flush() {
	batch := make([]*SpanData, 0, c.cfg.MaxBatchSize)
	for {
		select {
		case span := <-c.queue:
			batch = append(batch, span)
			if len(batch) >= c.cfg.MaxBatchSize {
				_ = c.transport.Send(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				_ = c.transport.Send(batch)
			}
			return
		}
	}
}

// flushLoop runs in a goroutine, periodically flushing the queue.
func (c *Client) flushLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.cfg.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
		case <-c.done:
			c.flush() // final flush
			return
		}
	}
}

// Shutdown signals the client to stop and waits for pending spans to be flushed.
func (c *Client) Shutdown() error {
	if c.cfg.Disabled {
		return nil
	}
	close(c.done)
	c.wg.Wait()
	return nil
}
