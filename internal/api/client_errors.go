package api

import (
	"encoding/json"
	"net/http"
	"sort"
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

	maxClientErrorAttrs     = 16
	maxClientErrorAttrKey   = 64
	maxClientErrorAttrValue = 512
)

// sensitiveAttrKeys are substrings of an attribute key whose value is replaced.
var sensitiveAttrKeys = []string{"password", "passwd", "secret", "token", "authorization", "cookie", "apikey", "api_key", "auth", "session"}

// clientErrorAttributes returns the attributes a browser sent, capped in count,
// key length and value length, with sensitive values replaced. The endpoint is
// unauthenticated, so nothing it receives is trusted. Keys are taken in sorted
// order so the same payload always keeps the same subset.
func clientErrorAttributes(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if len(out) >= maxClientErrorAttrs {
			break
		}
		key := truncate(k, maxClientErrorAttrKey)
		lower := strings.ToLower(key)
		value := truncate(in[k], maxClientErrorAttrValue)
		for _, marker := range sensitiveAttrKeys {
			if strings.Contains(lower, marker) {
				value = "[redacted]"
				break
			}
		}
		out[key] = value
	}
	return out
}

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
		"error.attributes", clientErrorAttributes(payload.Attributes),
		"source", "browser",
	)

	w.WriteHeader(http.StatusAccepted)
}
