package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
)

// fakeKeyStore holds one stored key under its hash.
type fakeKeyStore struct {
	hash   string
	record auth.APIKeyRecord
}

func (f fakeKeyStore) GetAPIKeyByHash(h string) (auth.APIKeyRecord, error) {
	if h == f.hash {
		return f.record, nil
	}
	return auth.APIKeyRecord{}, http.ErrNoCookie
}

func (fakeKeyStore) TouchAPIKey(int64) error { return nil }

func keyUseLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if line != "" && json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "api key used" {
			out = append(out, m)
		}
	}
	return out
}

// An accepted API key puts its row id on the access log and, once per sender,
// a line with the user agent and caller address. No key value is logged.
func TestAcceptedKeyLogsItsIDAndSender(t *testing.T) {
	key := strings.Join([]string{"row", "207", "http", "test"}, "-")
	store := fakeKeyStore{hash: auth.HashKey(key), record: auth.APIKeyRecord{ID: 207, ProjectID: 95, Scope: "ingest"}}
	a := auth.NewAuthorizer("", store, nil)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := loggingMiddleware(logger, func(*http.Request, int, time.Duration) {}, APIKeyMiddleware(a)(ok))

	call := func(ua, remote string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}"))
		req.Header.Set("X-SpanBarn-Api-Key", key)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Referer", "https://app.example.test/projects/7?token=page-secret")
		req.RemoteAddr = remote
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	call("OTel-OTLP-Exporter-JavaScript/1.30", "203.0.113.9:51234")
	call("OTel-OTLP-Exporter-JavaScript/1.30", "203.0.113.9:51240") // same sender, throttled
	call("curl/8.7.1", "198.51.100.4:40000")                        // another sender

	if strings.Contains(buf.String(), key) || strings.Contains(buf.String(), auth.HashKey(key)) ||
		strings.Contains(buf.String(), "page-secret") {
		t.Fatalf("log carries a key, its hash or a referer path:\n%s", buf.String())
	}
	lines := keyUseLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("got %d key-use lines, want 2 (one per sender):\n%s", len(lines), buf.String())
	}
	first := lines[0]
	if first["key_id"] != float64(207) || first["project_id"] != float64(95) || first["scope"] != "ingest" ||
		first["user_agent"] != "OTel-OTLP-Exporter-JavaScript/1.30" || first["caller_addr"] != "203.0.113.9" ||
		first["referer"] != "https://app.example.test" || first["level"] != "INFO" {
		t.Errorf("first line = %v", first)
	}
	if lines[1]["user_agent"] != "curl/8.7.1" || lines[1]["caller_addr"] != "198.51.100.4" {
		t.Errorf("second line = %v", lines[1])
	}

	var accessKeyIDs []float64
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "http" {
			id, _ := m["key_id"].(float64)
			accessKeyIDs = append(accessKeyIDs, id)
		}
	}
	if len(accessKeyIDs) != 3 || accessKeyIDs[0] != 207 || accessKeyIDs[2] != 207 {
		t.Errorf("access log key_id values = %v, want 207 on all three lines", accessKeyIDs)
	}
}

func TestKeyUseLogIsRateLimitedPerSender(t *testing.T) {
	var buf bytes.Buffer
	l := newKeyUseLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	t0 := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	at := func(d time.Duration) { l.now = func() time.Time { return t0.Add(d) } }
	ident := auth.Identity{KeyID: 207, ProjectID: 95, Scope: "ingest"}
	req := newKeyUseRequest("ua", "203.0.113.9", "/v1/traces", "", "")

	at(0)
	l.use(ident, req)
	at(time.Minute)
	l.use(ident, req)
	l.use(ident, req)
	at(keyUseInterval + time.Second)
	l.use(ident, req)

	lines := keyUseLines(t, &buf)
	if len(lines) != 2 || lines[0]["requests_since_last_line"] != float64(0) || lines[1]["requests_since_last_line"] != float64(2) {
		t.Fatalf("lines = %v", lines)
	}
}

// The gRPC transport logs the key's sender too, from the peer address.
func TestGRPCAcceptedKeyIsLogged(t *testing.T) {
	key := strings.Join([]string{"row", "207", "grpc", "test"}, "-")
	store := fakeKeyStore{hash: auth.HashKey(key), record: auth.APIKeyRecord{ID: 207, ProjectID: 95, Scope: "ingest"}}
	var buf bytes.Buffer
	s := &Server{authorizer: auth.NewAuthorizer("", store, nil), keyUse: newKeyUseLog(slog.New(slog.NewJSONHandler(&buf, nil)))}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+key, "user-agent", "grpc-go/1.70.0"))
	ctx = peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 4317}})
	info := &grpc.UnaryServerInfo{FullMethod: "/opentelemetry.proto.collector.trace.v1.TraceService/Export"}
	if _, err := grpcAuthInterceptor(s)(ctx, nil, info, func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("accepted key refused: %v", err)
	}
	if strings.Contains(buf.String(), key) {
		t.Fatalf("log carries the key:\n%s", buf.String())
	}
	lines := keyUseLines(t, &buf)
	if len(lines) != 1 || lines[0]["key_id"] != float64(207) || lines[0]["caller_addr"] != "203.0.113.9" ||
		lines[0]["user_agent"] != "grpc-go/1.70.0" || lines[0]["path"] != info.FullMethod {
		t.Fatalf("lines = %v", lines)
	}
}
