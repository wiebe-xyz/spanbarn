package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func constProbe(used float64, ok bool) DiskProbe {
	return func(context.Context) (float64, bool) { return used, ok }
}

func TestAdmissionAccepting(t *testing.T) {
	tests := []struct {
		name      string
		probe     DiskProbe
		threshold float64
		want      bool
	}{
		{"well under threshold", constProbe(0.10, true), 0.95, true},
		{"just under threshold", constProbe(0.949, true), 0.95, true},
		{"at threshold refuses", constProbe(0.95, true), 0.95, false},
		{"over threshold refuses", constProbe(0.99, true), 0.95, false},
		// A probe that cannot measure must fail OPEN. Taking ingest down
		// because statfs broke would be a worse outage than the one the guard
		// prevents.
		{"unmeasurable fails open", constProbe(0, false), 0.95, true},
		// A threshold outside (0,1) means "not opted in".
		{"threshold 0 disables", constProbe(0.99, true), 0, true},
		{"threshold 1 disables", constProbe(0.99, true), 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := NewAdmission(tt.probe, tt.threshold)
			if got, _ := a.Accepting(context.Background()); got != tt.want {
				t.Errorf("Accepting() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdmissionNilIsAlwaysAccepting(t *testing.T) {
	var a *Admission
	if got, _ := a.Accepting(context.Background()); !got {
		t.Error("nil Admission refused a request")
	}
	if a.Enabled() {
		t.Error("nil Admission reports Enabled")
	}
	if a.Shed() != 0 {
		t.Error("nil Admission reports shed requests")
	}
}

// TestAdmissionCachesProbe pins that admission does not syscall per request —
// it is consulted on the hottest path in the service and the figure moves over
// minutes, not milliseconds.
func TestAdmissionCachesProbe(t *testing.T) {
	var calls atomic.Int64
	a := NewAdmission(func(context.Context) (float64, bool) {
		calls.Add(1)
		return 0.1, true
	}, 0.95)

	for i := 0; i < 50; i++ {
		a.Accepting(context.Background())
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("probe called %d times for 50 requests, want 1 (cached)", got)
	}
}

// TestAdmissionProbeFailureKeepsLastReading is the important failure mode: a
// transient probe error during a real disk-full event must not re-open the
// gate and let the flood back in.
func TestAdmissionProbeFailureKeepsLastReading(t *testing.T) {
	var fail atomic.Bool
	a := NewAdmission(func(context.Context) (float64, bool) {
		if fail.Load() {
			return 0, false
		}
		return 0.99, true
	}, 0.95)
	a.ttl = 0 // re-probe every call

	if got, _ := a.Accepting(context.Background()); got {
		t.Fatal("expected refusal at 0.99 used")
	}

	fail.Store(true)
	if got, _ := a.Accepting(context.Background()); got {
		t.Error("probe failure re-opened the gate; expected the last reading (0.99) to stand")
	}
}

func TestAdmissionMiddleware(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	t.Run("passes through when healthy", func(t *testing.T) {
		a := NewAdmission(constProbe(0.10, true), 0.95)
		rec := httptest.NewRecorder()
		a.Middleware()(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/traces", nil))
		if rec.Code != http.StatusAccepted {
			t.Errorf("status = %d, want 202", rec.Code)
		}
	})

	t.Run("refuses with 503 and Retry-After when full", func(t *testing.T) {
		a := NewAdmission(constProbe(0.99, true), 0.95)
		rec := httptest.NewRecorder()
		a.Middleware()(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/traces", nil))

		// 503 not 429: this is server capacity, not a client quota, and OTLP
		// exporters treat 503+Retry-After as retryable backoff.
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got == "" {
			t.Error("missing Retry-After header")
		}
		if a.Shed() != 1 {
			t.Errorf("Shed() = %d, want 1", a.Shed())
		}
	})

	t.Run("nil controller is a transparent no-op", func(t *testing.T) {
		var a *Admission
		rec := httptest.NewRecorder()
		a.Middleware()(okHandler).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/traces", nil))
		if rec.Code != http.StatusAccepted {
			t.Errorf("status = %d, want 202", rec.Code)
		}
	})
}

func TestAdmissionUnaryInterceptor(t *testing.T) {
	handler := func(context.Context, any) (any, error) { return "served", nil }

	t.Run("passes through when healthy", func(t *testing.T) {
		a := NewAdmission(constProbe(0.10, true), 0.95)
		got, err := a.UnaryInterceptor()(context.Background(), nil, nil, handler)
		if err != nil || got != "served" {
			t.Errorf("got (%v, %v), want (served, nil)", got, err)
		}
	})

	t.Run("refuses with Unavailable when full", func(t *testing.T) {
		a := NewAdmission(constProbe(0.99, true), 0.95)
		_, err := a.UnaryInterceptor()(context.Background(), nil, nil, handler)
		if err == nil {
			t.Fatal("expected an error")
		}
		var se interface{ GRPCStatus() *status.Status }
		if !errors.As(err, &se) || se.GRPCStatus().Code() != codes.Unavailable {
			t.Errorf("error = %v, want codes.Unavailable", err)
		}
		if a.Shed() != 1 {
			t.Errorf("Shed() = %d, want 1", a.Shed())
		}
	})

	t.Run("nil controller passes through", func(t *testing.T) {
		var a *Admission
		got, err := a.UnaryInterceptor()(context.Background(), nil, nil, handler)
		if err != nil || got != "served" {
			t.Errorf("got (%v, %v), want (served, nil)", got, err)
		}
	})
}
