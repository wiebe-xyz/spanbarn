package api

import (
	"encoding/json"
	"net/http"
)

type clientErrorPayload struct {
	Message    string            `json:"message"`
	Type       string            `json:"type"`
	Stack      string            `json:"stack"`
	URL        string            `json:"url"`
	Attributes map[string]string `json:"attributes"`
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

	s.logger.Error("client error",
		"error.type", payload.Type,
		"error.message", payload.Message,
		"error.stack", payload.Stack,
		"error.url", payload.URL,
		"source", "browser",
	)

	w.WriteHeader(http.StatusAccepted)
}
