package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
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

// httpErrorMessage is the message the web app writes for a failed read:
// "HTTP <status> <method> <url>".
var httpErrorMessage = regexp.MustCompile(`^HTTP (\d{3}) ([A-Z]+) (\S+)`)

// clientErrorGrouping returns the log attributes BugBarn groups by for a
// browser HTTP error: method, route without its query string and status. Every
// browser report logs under the same message, so without them a 503 on the
// dashboard counts, a 503 on the heatmap and a 500 on the trace list share one
// issue (SPA-75). Reports of any other type return nil and keep their grouping.
func clientErrorGrouping(payload clientErrorPayload) []any {
	if payload.Attributes["type"] != "http_error" {
		return nil
	}
	m := httpErrorMessage.FindStringSubmatch(payload.Message)
	if m == nil {
		return nil
	}
	route := m[3]
	if u, err := url.Parse(route); err == nil {
		route = u.Path
	}
	return []any{
		"http.method", m[2],
		"http.route", truncate(route, maxClientErrorShort),
		"http.status_code", m[1],
	}
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
	args := []any{
		"error.type", truncate(payload.Type, maxClientErrorShort),
		"error.message", truncate(payload.Message, maxClientErrorMessage),
		"error.stack", truncate(payload.Stack, maxClientErrorStack),
		"error.url", truncate(payload.URL, maxClientErrorMessage),
		"error.attributes", clientErrorAttributes(payload.Attributes),
		"source", "browser",
	}
	s.logger.Error("client "+errType, append(args, clientErrorGrouping(payload)...)...)

	w.WriteHeader(http.StatusAccepted)
}
