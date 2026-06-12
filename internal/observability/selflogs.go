package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SelfLogsHandler is a slog.Handler that tees records to SpanBarn's own /v1/logs endpoint.
// It wraps an inner handler (so records still flow to stderr/BugBarn) and additionally
// batches them for OTLP HTTP export to the self-instrumentation endpoint.
type SelfLogsHandler struct {
	inner    slog.Handler
	exporter *selfLogsExporter
	attrs    []slog.Attr
}

// NewSelfLogsHandler creates a handler that forwards records to SpanBarn self-ingest.
func NewSelfLogsHandler(inner slog.Handler, endpoint, apiKey, service, version, env string) *SelfLogsHandler {
	return &SelfLogsHandler{
		inner:    inner,
		exporter: newSelfLogsExporter(endpoint, apiKey, service, version, env),
	}
}

func (h *SelfLogsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *SelfLogsHandler) Handle(ctx context.Context, r slog.Record) error {
	innerErr := h.inner.Handle(ctx, r)

	attrs := make(map[string]string, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		attrs[a.Key] = slogValueToString(a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = slogValueToString(a.Value)
		return true
	})

	ts := r.Time.UnixNano()
	if ts <= 0 {
		ts = time.Now().UnixNano()
	}

	h.exporter.enqueue(selfLogRec{
		timeNano: ts,
		sevNum:   slogToOTLPSeverity(r.Level),
		sevText:  r.Level.String(),
		body:     r.Message,
		attrs:    attrs,
	})

	return innerErr
}

func (h *SelfLogsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(merged, h.attrs)
	copy(merged[len(h.attrs):], attrs)
	return &SelfLogsHandler{
		inner:    h.inner.WithAttrs(attrs),
		exporter: h.exporter,
		attrs:    merged,
	}
}

func (h *SelfLogsHandler) WithGroup(name string) slog.Handler {
	return &SelfLogsHandler{
		inner:    h.inner.WithGroup(name),
		exporter: h.exporter,
		attrs:    h.attrs,
	}
}

// Shutdown flushes buffered records and stops the background goroutine.
func (h *SelfLogsHandler) Shutdown() {
	h.exporter.shutdown()
}

// ---- exporter ----

type selfLogRec struct {
	timeNano int64
	sevNum   int32
	sevText  string
	body     string
	attrs    map[string]string
}

type selfLogsExporter struct {
	url      string
	apiKey   string
	resource []otlpSelfKV
	hc       *http.Client
	queue    chan selfLogRec
	done     chan struct{}
	wg       sync.WaitGroup
}

func newSelfLogsExporter(endpoint, apiKey, service, version, env string) *selfLogsExporter {
	resource := []otlpSelfKV{
		{Key: "service.name", Value: otlpSelfAV{StringValue: service}},
	}
	if env != "" {
		resource = append(resource, otlpSelfKV{Key: "deployment.environment", Value: otlpSelfAV{StringValue: env}})
	}
	if version != "" {
		resource = append(resource, otlpSelfKV{Key: "service.version", Value: otlpSelfAV{StringValue: version}})
	}

	e := &selfLogsExporter{
		url:      strings.TrimRight(endpoint, "/") + "/v1/logs",
		apiKey:   apiKey,
		resource: resource,
		hc:       &http.Client{Timeout: 5 * time.Second},
		queue:    make(chan selfLogRec, 512),
		done:     make(chan struct{}),
	}
	e.wg.Add(1)
	go e.run()
	return e
}

func (e *selfLogsExporter) enqueue(r selfLogRec) {
	select {
	case e.queue <- r:
	default:
	}
}

func (e *selfLogsExporter) run() {
	defer e.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var batch []selfLogRec
	for {
		select {
		case r := <-e.queue:
			batch = append(batch, r)
			if len(batch) >= 100 {
				e.send(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				e.send(batch)
				batch = batch[:0]
			}
		case <-e.done:
			for {
				select {
				case r := <-e.queue:
					batch = append(batch, r)
				default:
					if len(batch) > 0 {
						e.send(batch)
					}
					return
				}
			}
		}
	}
}

func (e *selfLogsExporter) send(batch []selfLogRec) {
	recs := make([]otlpSelfLogRec, 0, len(batch))
	for _, r := range batch {
		ts := strconv.FormatInt(r.timeNano, 10)
		rec := otlpSelfLogRec{
			TimeUnixNano:         ts,
			ObservedTimeUnixNano: ts,
			SeverityNumber:       r.sevNum,
			SeverityText:         r.sevText,
			Body:                 otlpSelfAV{StringValue: r.body},
		}
		for k, v := range r.attrs {
			rec.Attributes = append(rec.Attributes, otlpSelfKV{Key: k, Value: otlpSelfAV{StringValue: v}})
		}
		recs = append(recs, rec)
	}

	payload := otlpSelfLogsPayload{
		ResourceLogs: []otlpSelfResourceLog{{
			Resource:  otlpSelfResource{Attributes: e.resource},
			ScopeLogs: []otlpSelfScopeLog{{LogRecords: recs}},
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SpanBarn-Api-Key", e.apiKey)
	req.Header.Set("User-Agent", selfInstrumentUA)

	resp, err := e.hc.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func (e *selfLogsExporter) shutdown() {
	close(e.done)
	e.wg.Wait()
}

// ---- OTLP JSON types (logs payload) ----

type otlpSelfLogsPayload struct {
	ResourceLogs []otlpSelfResourceLog `json:"resourceLogs"`
}

type otlpSelfResourceLog struct {
	Resource  otlpSelfResource  `json:"resource"`
	ScopeLogs []otlpSelfScopeLog `json:"scopeLogs"`
}

type otlpSelfResource struct {
	Attributes []otlpSelfKV `json:"attributes"`
}

type otlpSelfScopeLog struct {
	LogRecords []otlpSelfLogRec `json:"logRecords"`
}

type otlpSelfLogRec struct {
	TimeUnixNano         string       `json:"timeUnixNano"`
	ObservedTimeUnixNano string       `json:"observedTimeUnixNano"`
	SeverityNumber       int32        `json:"severityNumber"`
	SeverityText         string       `json:"severityText"`
	Body                 otlpSelfAV   `json:"body"`
	Attributes           []otlpSelfKV `json:"attributes,omitempty"`
}

type otlpSelfKV struct {
	Key   string     `json:"key"`
	Value otlpSelfAV `json:"value"`
}

type otlpSelfAV struct {
	StringValue string `json:"stringValue"`
}

// ---- helpers ----

func slogToOTLPSeverity(l slog.Level) int32 {
	switch {
	case l >= slog.LevelError:
		return 17
	case l >= slog.LevelWarn:
		return 13
	case l >= slog.LevelInfo:
		return 9
	default:
		return 5
	}
}

func slogValueToString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'f', -1, 64)
	case slog.KindBool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	case slog.KindGroup:
		m := make(map[string]string, len(v.Group()))
		for _, a := range v.Group() {
			m[a.Key] = slogValueToString(a.Value)
		}
		b, _ := json.Marshal(m)
		return string(b)
	default:
		if v.Any() == nil {
			return ""
		}
		return fmt.Sprintf("%v", v.Any())
	}
}
