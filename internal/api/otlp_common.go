package api

import (
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// readOTLPBody reads the full request body, mapping a MaxBytesReader overflow to
// 413 and any other read error to 400. Returns ok=false after writing the error
// response, so callers just `if !ok { return }`.
func readOTLPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "failed to read body", err.Error())
		return nil, false
	}
	return body, true
}

// decodeOTLP unmarshals an OTLP export request from body into msg, choosing
// protojson vs binary protobuf by the Content-Type header. Returns ok=false
// after writing the error response.
func decodeOTLP(w http.ResponseWriter, r *http.Request, body []byte, msg proto.Message) bool {
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		if err := protojson.Unmarshal(body, msg); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON", err.Error())
			return false
		}
		return true
	}
	if err := proto.Unmarshal(body, msg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid protobuf", err.Error())
		return false
	}
	return true
}

// writeOTLPResponse marshals an OTLP export response with 200, choosing
// protojson vs binary protobuf by the Accept header (defaulting to protobuf).
func writeOTLPResponse(w http.ResponseWriter, r *http.Request, resp proto.Message) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		data, _ := protojson.Marshal(resp)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	data, _ := proto.Marshal(resp)
	_, _ = w.Write(data)
}
