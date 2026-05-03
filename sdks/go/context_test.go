package spanbarn

import (
	"context"
	"net/http"
	"testing"
)

func TestSpanContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	sc := spanContext{TraceID: "abcd1234abcd1234abcd1234abcd1234", SpanID: "1234abcd1234abcd"}

	ctx = withSpanContext(ctx, sc)
	got, ok := spanContextFromContext(ctx)
	if !ok {
		t.Fatal("expected span context in context")
	}
	if got.TraceID != sc.TraceID {
		t.Errorf("traceID: got %q, want %q", got.TraceID, sc.TraceID)
	}
	if got.SpanID != sc.SpanID {
		t.Errorf("spanID: got %q, want %q", got.SpanID, sc.SpanID)
	}
}

func TestSpanContextFromContextEmpty(t *testing.T) {
	_, ok := spanContextFromContext(context.Background())
	if ok {
		t.Error("expected no span context in empty context")
	}
}

func TestMakeTraceparent(t *testing.T) {
	tp := MakeTraceparent("abcd1234abcd1234abcd1234abcd1234", "1234abcd1234abcd")
	expected := "00-abcd1234abcd1234abcd1234abcd1234-1234abcd1234abcd-01"
	if tp != expected {
		t.Errorf("got %q, want %q", tp, expected)
	}
}

func TestParseTraceparent(t *testing.T) {
	traceID, spanID, ok := ParseTraceparent("00-abcd1234abcd1234abcd1234abcd1234-1234abcd1234abcd-01")
	if !ok {
		t.Fatal("expected parsing to succeed")
	}
	if traceID != "abcd1234abcd1234abcd1234abcd1234" {
		t.Errorf("traceID: got %q", traceID)
	}
	if spanID != "1234abcd1234abcd" {
		t.Errorf("spanID: got %q", spanID)
	}
}

func TestParseTraceparentInvalid(t *testing.T) {
	cases := []string{
		"",
		"garbage",
		"01-abc-def-01",                // wrong version length for parts
		"00-short-1234abcd1234abcd-01", // traceID too short
		"00-abcd1234abcd1234abcd1234abcd1234-short-01", // spanID too short
		"01-abcd1234abcd1234abcd1234abcd1234-1234abcd1234abcd-01", // wrong version
	}
	for _, c := range cases {
		_, _, ok := ParseTraceparent(c)
		if ok {
			t.Errorf("expected parsing to fail for %q", c)
		}
	}
}

func TestInjectExtractTraceparent(t *testing.T) {
	h := make(http.Header)
	traceID := "abcd1234abcd1234abcd1234abcd1234"
	spanID := "1234abcd1234abcd"

	InjectTraceparent(h, traceID, spanID)

	gotTraceID, gotSpanID, ok := ExtractTraceparent(h)
	if !ok {
		t.Fatal("expected extraction to succeed")
	}
	if gotTraceID != traceID {
		t.Errorf("traceID: got %q, want %q", gotTraceID, traceID)
	}
	if gotSpanID != spanID {
		t.Errorf("spanID: got %q, want %q", gotSpanID, spanID)
	}
}

func TestExtractTraceparentMissing(t *testing.T) {
	h := make(http.Header)
	_, _, ok := ExtractTraceparent(h)
	if ok {
		t.Error("expected extraction to fail on empty headers")
	}
}
