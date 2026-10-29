package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wiebe-xyz/spanbarn/internal/auth"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/spool"
)

const (
	goodIngestKey  = "9f2c4a7e1b3d5f6a8c0e2b4d6f8a1c3e5b7d9f0a2c4e6b8d0f1a3c5e7b9d2f4a"
	staleIngestKey = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b"
	otherBadKey    = "badkey-with-a-recognisable-shape-0123456789"
)

// rejectedLines parses the "ingest auth rejected" lines out of a JSON log.
func rejectedLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if line == "" || json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m["msg"] == "ingest auth rejected" {
			out = append(out, m)
		}
	}
	return out
}

func assertNoRawKeys(t *testing.T, log string) {
	t.Helper()
	for _, k := range []string{goodIngestKey, staleIngestKey, otherBadKey} {
		if strings.Contains(log, k) {
			t.Fatalf("log output contains a raw key:\n%s", log)
		}
	}
}

func newRejectedAuthTestServer(t *testing.T, buf *bytes.Buffer) http.Handler {
	t.Helper()
	sp, err := spool.NewSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	h := ingest.NewHandler(ingest.NewQueue(64), sp, 0, logger)
	a := auth.NewAuthorizer(auth.HashKey(goodIngestKey), nil, nil)
	srv := NewServerWithQuery(ServerConfig{MaxBodyBytes: 1 << 20, Version: "test"}, h, nil, nil, logger, WithAuthorizer(a))
	return srv.Handler()
}

// TestRejectedIngestAuthNeverLogsTheKey drives the full handler: every refused
// OTLP request logs its key's sha256 prefix, user-agent and path, and no key
// appears anywhere in the log output, refused or accepted.
func TestRejectedIngestAuthNeverLogsTheKey(t *testing.T) {
	var buf bytes.Buffer
	h := newRejectedAuthTestServer(t, &buf)

	send := func(header, value string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "OTel-OTLP-Exporter-Go/1.38.0")
		req.Header.Set("Origin", "https://app.example.test")
		req.Header.Set("Referer", "https://app.example.test/projects/7?token=page-secret-123#tab")
		if header != "" {
			req.Header.Set(header, value)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send("Authorization", "Bearer "+staleIngestKey); code != http.StatusUnauthorized {
		t.Fatalf("stale bearer key: status %d", code)
	}
	if code := send("X-SpanBarn-Api-Key", otherBadKey); code != http.StatusUnauthorized {
		t.Fatalf("bad header key: status %d", code)
	}
	if code := send("", ""); code != http.StatusUnauthorized {
		t.Fatalf("no key: status %d", code)
	}
	if code := send("X-SpanBarn-Api-Key", goodIngestKey); code == http.StatusUnauthorized {
		t.Fatalf("good key refused")
	}

	assertNoRawKeys(t, buf.String())
	if strings.Contains(buf.String(), "page-secret-123") || strings.Contains(buf.String(), "/projects/7") {
		t.Fatalf("log output carries the Referer's path or query:\n%s", buf.String())
	}
	lines := rejectedLines(t, &buf)
	want := []string{keyFingerprint(staleIngestKey), keyFingerprint(otherBadKey), "none"}
	if len(lines) != len(want) {
		t.Fatalf("got %d rejected lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, m := range lines {
		if m["key_sha256_prefix"] != want[i] || m["path"] != "/v1/traces" ||
			m["user_agent"] != "OTel-OTLP-Exporter-Go/1.38.0" || m["level"] != "INFO" ||
			m["origin"] != "https://app.example.test" || m["referer"] != "https://app.example.test" {
			t.Errorf("line %d = %v", i, m)
		}
	}
	if fp := want[0]; len(fp) != 8 || strings.Contains(staleIngestKey, fp) {
		t.Errorf("fingerprint %q is not an 8-hex hash prefix", fp)
	}
}

// TestRejectedAuthLogIsRateLimitedPerKey checks one line per key per interval,
// with the refusals in between counted on the next line.
func TestRejectedAuthLogIsRateLimitedPerKey(t *testing.T) {
	var buf bytes.Buffer
	l := newRejectedAuthLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	t0 := time.Date(2026, 10, 29, 1, 22, 0, 0, time.UTC)
	at := func(d time.Duration) { l.now = func() time.Time { return t0.Add(d) } }

	at(0)
	l.reject(staleIngestKey, newRejectedRequest("ua", "/v1/traces", "", ""))
	at(10 * time.Second)
	l.reject(staleIngestKey, newRejectedRequest("ua", "/v1/traces", "", ""))
	at(30 * time.Second)
	l.reject(staleIngestKey, newRejectedRequest("ua", "/v1/traces", "", ""))
	l.reject(otherBadKey, newRejectedRequest("ua", "/v1/traces", "", ""))
	at(61 * time.Second)
	l.reject(staleIngestKey, newRejectedRequest("ua", "/v1/traces", "", ""))

	lines := rejectedLines(t, &buf)
	got := make([]string, 0, len(lines))
	for _, m := range lines {
		got = append(got, m["key_sha256_prefix"].(string)+" "+jsonNumber(m["suppressed"]))
	}
	want := []string{
		keyFingerprint(staleIngestKey) + " 0",
		keyFingerprint(otherBadKey) + " 0",
		keyFingerprint(staleIngestKey) + " 2",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	assertNoRawKeys(t, buf.String())
}

func jsonNumber(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestGRPCRejectedAuthIsLogged covers the gRPC transport: a refused key logs
// its prefix, user-agent and method, and never the key.
func TestGRPCRejectedAuthIsLogged(t *testing.T) {
	var buf bytes.Buffer
	s := &Server{apiKey: goodIngestKey, rejectedAuth: newRejectedAuthLog(slog.New(slog.NewJSONHandler(&buf, nil)))}
	md := metadata.Pairs("authorization", "Bearer "+staleIngestKey, "user-agent", "grpc-go/1.70.0")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/opentelemetry.proto.collector.trace.v1.TraceService/Export"}
	called := false
	_, err := grpcAuthInterceptor(s)(ctx, nil, info, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if err == nil || called {
		t.Fatalf("stale key accepted: err=%v called=%v", err, called)
	}
	assertNoRawKeys(t, buf.String())
	lines := rejectedLines(t, &buf)
	if len(lines) != 1 || lines[0]["key_sha256_prefix"] != keyFingerprint(staleIngestKey) ||
		lines[0]["path"] != info.FullMethod || lines[0]["user_agent"] != "grpc-go/1.70.0" {
		t.Fatalf("lines = %v", lines)
	}
}

// TestSchemeAndHostDropsPathAndQuery pins the Referer reduction.
func TestSchemeAndHostDropsPathAndQuery(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.example.test/a/b?token=x#f": "https://app.example.test",
		"http://localhost:5173/":                 "http://localhost:5173",
		"":                                       "",
		"/relative/path":                         "",
		"not a url %zz":                          "",
	} {
		if got := schemeAndHost(in); got != want {
			t.Errorf("schemeAndHost(%q) = %q, want %q", in, got, want)
		}
	}
}
