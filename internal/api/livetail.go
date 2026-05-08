package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/wiebe-xyz/spanbarn/internal/livetail"
)

type liveTailHandler struct {
	broadcaster *livetail.Broadcaster
}

func (h *liveTailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported", "")
		return
	}

	serviceFilter := r.URL.Query().Get("service")
	statusFilter := r.URL.Query().Get("status")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := h.broadcaster.Subscribe(256)
	defer h.broadcaster.Unsubscribe(sub)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case record := <-sub.C:
			if serviceFilter != "" && record.Service != serviceFilter {
				continue
			}
			if statusFilter != "" && record.Status != statusFilter {
				continue
			}

			data, err := json.Marshal(record)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
