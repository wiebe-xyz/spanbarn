package observability

import (
	"context"
	"log/slog"
)

// BugBarnHandler is a slog.Handler that wraps another handler and forwards
// WARN and ERROR level records to BugBarn as structured logs.
type BugBarnHandler struct {
	inner  slog.Handler
	client *BugBarnClient
	attrs  []slog.Attr
	groups []string
}

// NewBugBarnHandler creates a slog handler that tees log records to BugBarn.
// Records at WARN level and above are sent as structured log events.
func NewBugBarnHandler(inner slog.Handler, client *BugBarnClient) *BugBarnHandler {
	return &BugBarnHandler{
		inner:  inner,
		client: client,
	}
}

func (h *BugBarnHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *BugBarnHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && h.client != nil {
		attrs := make(map[string]any)
		for _, a := range h.attrs {
			attrs[a.Key] = a.Value.Any()
		}
		r.Attrs(func(a slog.Attr) bool {
			v := a.Value.Any()
			if err, ok := v.(error); ok {
				attrs[a.Key] = err.Error()
			} else {
				attrs[a.Key] = v
			}
			return true
		})

		level := "WARN"
		if r.Level >= slog.LevelError {
			level = "ERROR"
		}

		h.client.CaptureLog(level, r.Message, attrs)
	}
	return h.inner.Handle(ctx, r)
}

func (h *BugBarnHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BugBarnHandler{
		inner:  h.inner.WithAttrs(attrs),
		client: h.client,
		attrs:  append(h.attrs, attrs...),
		groups: h.groups,
	}
}

func (h *BugBarnHandler) WithGroup(name string) slog.Handler {
	return &BugBarnHandler{
		inner:  h.inner.WithGroup(name),
		client: h.client,
		attrs:  h.attrs,
		groups: append(h.groups, name),
	}
}
