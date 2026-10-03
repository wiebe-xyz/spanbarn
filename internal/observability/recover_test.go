package observability

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSafeGoReportsPanicAsException(t *testing.T) {
	var exceptions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exceptions.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := NewBugBarnClient(BugBarnConfig{Endpoint: srv.URL, APIKey: "k", Project: "spanbarn"})
	defaultClient.Store(c)
	defer defaultClient.Store(nil)
	defer c.Shutdown()

	var wg sync.WaitGroup
	SafeGo("test", &wg, func() { panic("boom") })
	wg.Wait()
	c.Flush()

	if exceptions.Load() != 1 {
		t.Fatalf("expected 1 exception delivered, got %d", exceptions.Load())
	}
}

func TestClientCountsDroppedEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewBugBarnClient(BugBarnConfig{Endpoint: srv.URL, APIKey: "bad", Project: "spanbarn"})
	c.CaptureLog("ERROR", "x", nil)
	c.Shutdown()

	if c.Dropped() != 1 {
		t.Fatalf("expected 1 dropped event, got %d", c.Dropped())
	}
}
