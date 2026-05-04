package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// BugBarnClient sends errors and structured logs to a BugBarn instance.
type BugBarnClient struct {
	endpoint string
	apiKey   string
	project  string
	env      string
	version  string
	client   *http.Client

	queue chan bugbarnEvent
	done  chan struct{}
	wg    sync.WaitGroup
}

type bugbarnEvent struct {
	Type       string            `json:"type"`
	Timestamp  string            `json:"timestamp"`
	Level      string            `json:"level,omitempty"`
	Message    string            `json:"message"`
	Attributes map[string]any    `json:"attributes,omitempty"`
	Exception  *bugbarnException `json:"exception,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type bugbarnException struct {
	Type       string          `json:"type"`
	Value      string          `json:"value"`
	Stacktrace []bugbarnFrame `json:"stacktrace,omitempty"`
}

type bugbarnFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
}

// BugBarnConfig holds the configuration for a BugBarn client.
type BugBarnConfig struct {
	Endpoint    string
	APIKey      string
	Project     string
	Environment string
	Version     string
}

// NewBugBarnClient creates a new client that batches events to BugBarn.
func NewBugBarnClient(cfg BugBarnConfig) *BugBarnClient {
	c := &BugBarnClient{
		endpoint: cfg.Endpoint,
		apiKey:   cfg.APIKey,
		project:  cfg.Project,
		env:      cfg.Environment,
		version:  cfg.Version,
		client:   &http.Client{Timeout: 5 * time.Second},
		queue:    make(chan bugbarnEvent, 256),
		done:     make(chan struct{}),
	}
	c.wg.Add(1)
	go c.flushLoop()
	return c
}

// CaptureError sends an exception event to BugBarn.
func (c *BugBarnClient) CaptureError(err error, attrs map[string]any) {
	frames := captureStack(3)
	ev := bugbarnEvent{
		Type:      "exception",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   err.Error(),
		Exception: &bugbarnException{
			Type:       fmt.Sprintf("%T", err),
			Value:      err.Error(),
			Stacktrace: frames,
		},
		Attributes: attrs,
		Tags:       c.baseTags(),
	}
	c.enqueue(ev)
}

// CaptureLog sends a structured log event to BugBarn.
func (c *BugBarnClient) CaptureLog(level, msg string, attrs map[string]any) {
	ev := bugbarnEvent{
		Type:       "log",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Level:      level,
		Message:    msg,
		Attributes: attrs,
		Tags:       c.baseTags(),
	}
	c.enqueue(ev)
}

func (c *BugBarnClient) baseTags() map[string]string {
	tags := map[string]string{
		"service": "spanbarn",
	}
	if c.env != "" {
		tags["environment"] = c.env
	}
	if c.version != "" {
		tags["version"] = c.version
	}
	return tags
}

func (c *BugBarnClient) enqueue(ev bugbarnEvent) {
	select {
	case c.queue <- ev:
	default:
	}
}

func (c *BugBarnClient) flushLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
		case <-c.done:
			c.flush()
			return
		}
	}
}

func (c *BugBarnClient) flush() {
	for {
		select {
		case ev := <-c.queue:
			c.send(ev)
		default:
			return
		}
	}
}

func (c *BugBarnClient) send(ev bugbarnEvent) {
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}

	endpoint := c.endpoint + "/api/v1/events"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BugBarn-Api-Key", c.apiKey)
	req.Header.Set("X-BugBarn-Project", c.project)

	resp, err := c.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// Shutdown flushes remaining events and stops the background loop.
func (c *BugBarnClient) Shutdown() {
	close(c.done)
	c.wg.Wait()
}

func captureStack(skip int) []bugbarnFrame {
	var frames []bugbarnFrame
	pcs := make([]uintptr, 16)
	n := runtime.Callers(skip, pcs)
	runtimeFrames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := runtimeFrames.Next()
		if frame.Function == "" {
			break
		}
		frames = append(frames, bugbarnFrame{
			Function: frame.Function,
			File:     frame.File,
			Line:     frame.Line,
		})
		if !more {
			break
		}
	}
	return frames
}
