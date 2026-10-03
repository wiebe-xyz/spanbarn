package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/wiebe-xyz/spanbarn/internal/service"
)

type exportHandlers struct {
	svc *service.ExportService
}

func (h *exportHandlers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	from, to, err := parseTimeRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid time range", err.Error())
		return
	}

	filter := service.SpanFilter{
		From:    from,
		To:      to,
		Service: r.URL.Query().Get("service"),
		Status:  r.URL.Query().Get("status"),
		Limit:   100000,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", "attachment; filename=spans.ndjson")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	_ = h.svc.StreamSpans(filter, func(s service.Span) error {
		if err := enc.Encode(s); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
}
