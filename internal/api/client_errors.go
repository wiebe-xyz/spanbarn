package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

type clientErrorPayload struct {
	Message    string            `json:"message"`
	Type       string            `json:"type"`
	Stack      string            `json:"stack"`
	URL        string            `json:"url"`
	Attributes map[string]string `json:"attributes"`
}

// Field caps for the unauthenticated crash-report endpoint.
const (
	maxClientErrorShort   = 128
	maxClientErrorMessage = 1024
	maxClientErrorStack   = 8192
)

func truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return strings.ToValidUTF8(v[:max], "")
}

func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "")
		return
	}

	var payload clientErrorPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON", "")
		return
	}

	errType := payload.Type
	if errType == "" {
		errType = "Error"
	}
	errType = truncate(errType, maxClientErrorShort)
	s.logger.Error("client "+errType,
		"error.type", truncate(payload.Type, maxClientErrorShort),
		"error.message", truncate(payload.Message, maxClientErrorMessage),
		"error.stack", truncate(payload.Stack, maxClientErrorStack),
		"error.url", truncate(payload.URL, maxClientErrorMessage),
		"source", "browser",
	)

	w.WriteHeader(http.StatusAccepted)
}
