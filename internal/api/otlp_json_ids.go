package api

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
)

// otlpJSONIDLength is the hex length of each OTLP id field.
var otlpJSONIDLength = map[string]int{
	"traceId":      32,
	"spanId":       16,
	"parentSpanId": 16,
}

// hexIDsToBase64 rewrites the trace and span ids of an OTLP/JSON body from
// hex, which the OTLP/JSON encoding specifies, to base64, which protojson
// expects for bytes fields. Without it protojson base64-decodes a 32 character
// hex trace id into 24 garbage bytes, and no browser span ever shares a trace
// with the server span it called. An id that is not hex of the right length is
// left alone, so base64 senders keep working (a base64 trace id is 24
// characters and a base64 span id 12, so the two never collide). A body that
// does not parse is returned unchanged for protojson to reject.
func hexIDsToBase64(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return body
	}
	if !rewriteHexIDs(doc) {
		return body
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// rewriteHexIDs walks v and converts matching id strings in place. It reports
// whether it changed anything.
func rewriteHexIDs(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if want, ok := otlpJSONIDLength[k]; ok {
				if s, isString := child.(string); isString && len(s) == want {
					if raw, err := hex.DecodeString(s); err == nil {
						t[k] = base64.StdEncoding.EncodeToString(raw)
						changed = true
					}
				}
				continue
			}
			if rewriteHexIDs(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range t {
			if rewriteHexIDs(child) {
				changed = true
			}
		}
	}
	return changed
}
