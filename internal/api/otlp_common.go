package api

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// readOTLPBody reads the full request body, transparently gunzipping it when
// the client sent Content-Encoding: gzip (the OTLP spec requires servers to
// accept it). The size limit applies to the wire bytes (via the server-wide
// maxBodyBytesMiddleware) and again to the decompressed bytes, so a small
// compressed body cannot inflate past it.
//
// An oversize body maps to 413, an unsupported encoding to 415, and any other
// read error (including a corrupt gzip stream) to 400. Returns ok=false after
// writing the error response, so callers just `if !ok { return }`.
func (s *Server) readOTLPBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	src := r.Body
	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			writeOTLPReadError(w, err)
			return nil, false
		}
		defer zr.Close()
		src = http.MaxBytesReader(w, zr, s.maxBodyBytes)
	default:
		writeError(w, http.StatusUnsupportedMediaType, "unsupported content encoding", enc)
		return nil, false
	}

	body, err := io.ReadAll(src)
	if err != nil {
		writeOTLPReadError(w, err)
		return nil, false
	}
	return body, true
}

func writeOTLPReadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
		return
	}
	writeError(w, http.StatusBadRequest, "failed to read body", err.Error())
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
