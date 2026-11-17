package api

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func decodeJSONTrace(t *testing.T, body string) *collectorpb.ExportTraceServiceRequest {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	var req collectorpb.ExportTraceServiceRequest
	if !decodeOTLP(w, r, []byte(body), &req) {
		t.Fatalf("decode failed: %d %s", w.Code, w.Body.String())
	}
	return &req
}

func TestOTLPJSONHexIDs(t *testing.T) {
	const trace = "c06b8e8f3d2a4b5c9e1f0a7b6c5d4e3f"
	const span = "a1b2c3d4e5f60718"
	const parent = "0102030405060708"
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"` + trace +
		`","spanId":"` + span + `","parentSpanId":"` + parent + `","name":"POST /admin/books","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`
	spans := decodeJSONTrace(t, body).GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()
	got := spans[0]
	if hexOf(got.GetTraceId()) != trace || hexOf(got.GetSpanId()) != span || hexOf(got.GetParentSpanId()) != parent {
		t.Fatalf("ids = %s %s %s, want %s %s %s", hexOf(got.GetTraceId()), hexOf(got.GetSpanId()), hexOf(got.GetParentSpanId()), trace, span, parent)
	}
	if len(got.GetTraceId()) != 16 || len(got.GetSpanId()) != 8 {
		t.Fatalf("id byte lengths = %d, %d, want 16, 8", len(got.GetTraceId()), len(got.GetSpanId()))
	}
}

func TestOTLPJSONBase64IDsStillWork(t *testing.T) {
	// 16 bytes 00..0f and 8 bytes 00..07 as base64.
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"AAECAwQFBgcICQoLDA0ODw==","spanId":"AAECAwQFBgc=","name":"x"}]}]}]}`
	got := decodeJSONTrace(t, body).GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0]
	if hexOf(got.GetTraceId()) != "000102030405060708090a0b0c0d0e0f" || hexOf(got.GetSpanId()) != "0001020304050607" {
		t.Fatalf("base64 ids changed: %s %s", hexOf(got.GetTraceId()), hexOf(got.GetSpanId()))
	}
}

func TestHexIDsToBase64LeavesOtherBodiesAlone(t *testing.T) {
	for _, in := range []string{`not json`, `{"resourceSpans":[]}`, `{"traceId":"zz6b8e8f3d2a4b5c9e1f0a7b6c5d4e3f"}`} {
		if got := string(hexIDsToBase64([]byte(in))); got != in {
			t.Errorf("hexIDsToBase64(%q) = %q, want it unchanged", in, got)
		}
	}
}
