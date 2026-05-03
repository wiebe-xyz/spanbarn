package alert

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendWebhook(t *testing.T) {
	var received AlertPayload
	var gotContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewDefaultNotifier(NotifierConfig{}, slog.Default())
	payload := AlertPayload{
		AlertID:     1,
		Service:     "web",
		Operation:   "GET /api",
		Type:        "latency",
		Threshold:   100.0,
		Current:     200.0,
		Average:     50.0,
		TriggeredAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}

	err := notifier.SendWebhook(srv.URL, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %s", gotContentType)
	}
	if received.AlertID != 1 {
		t.Fatalf("expected alertId=1, got %d", received.AlertID)
	}
	if received.Current != 200.0 {
		t.Fatalf("expected current=200.0, got %f", received.Current)
	}
	if received.Service != "web" {
		t.Fatalf("expected service=web, got %s", received.Service)
	}
}

func TestSendWebhookFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := NewDefaultNotifier(NotifierConfig{}, slog.Default())
	payload := AlertPayload{
		AlertID: 1,
		Service: "web",
		Type:    "latency",
	}

	err := notifier.SendWebhook(srv.URL, payload)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
