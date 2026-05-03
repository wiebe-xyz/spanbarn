package spanbarn

import (
	"fmt"
	"net/http"
)

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// HTTPMiddleware wraps incoming HTTP requests with server spans.
// It extracts traceparent from incoming headers and injects span context
// into the request context.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultMu.Lock()
		c := defaultClient
		defaultMu.Unlock()

		if c == nil || c.cfg.Disabled {
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		spanName := fmt.Sprintf("%s %s", r.Method, r.URL.Path)

		var opts []SpanOption
		opts = append(opts, WithKind("server"))
		opts = append(opts, WithAttributes(map[string]interface{}{
			"http.method": r.Method,
			"http.route":  r.URL.Path,
		}))

		// Extract incoming traceparent
		if traceID, spanID, ok := ExtractTraceparent(r.Header); ok {
			// Create child span under incoming trace
			ctx = withSpanContext(ctx, spanContext{TraceID: traceID, SpanID: spanID})
		}

		ctx, span := c.Start(ctx, spanName, opts...)

		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		span.SetAttribute("http.status_code", rec.statusCode)
		if rec.statusCode >= 500 {
			span.SetStatus("error")
		}
		span.End()
	})
}

// httpTransport wraps an http.RoundTripper to create client spans for outgoing requests.
type httpTransport struct {
	base http.RoundTripper
}

// NewHTTPTransport wraps the given RoundTripper (or http.DefaultTransport if nil)
// to automatically create client spans and inject traceparent headers.
func NewHTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &httpTransport{base: base}
}

func (t *httpTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	defaultMu.Lock()
	c := defaultClient
	defaultMu.Unlock()

	if c == nil || c.cfg.Disabled {
		return t.base.RoundTrip(req)
	}

	spanName := fmt.Sprintf("HTTP %s %s", req.Method, req.URL.Host)

	ctx, span := c.Start(req.Context(), spanName,
		WithKind("client"),
		WithAttributes(map[string]interface{}{
			"http.method": req.Method,
			"http.url":    req.URL.String(),
		}),
	)
	defer span.End()

	// Inject traceparent into outgoing headers
	InjectTraceparent(req.Header, span.data.TraceID, span.data.SpanID)

	req = req.WithContext(ctx)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		span.Error(err)
		return resp, err
	}

	span.SetAttribute("http.status_code", resp.StatusCode)
	if resp.StatusCode >= 400 {
		span.SetStatus("error")
	}
	return resp, nil
}
