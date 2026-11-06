// Package bugbarntest records what spanbarn's BugBarn handler sends, for tests
// that check a log line reaches BugBarn and files an event there.
package bugbarntest

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/observability"
)

// Event is one event BugBarn received.
type Event struct {
	Level      string         `json:"level"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes"`
}

// Recorder is a stand-in BugBarn server.
type Recorder struct {
	client *observability.BugBarnClient
	stop   sync.Once
	mu     sync.Mutex
	events []Event
}

// Logger returns a logger that writes to inner through the BugBarn handler
// production uses, with its client pointed at a test server, and the recorder
// of what that server receives. The handler is enabled where inner is, so a
// discarding inner handler has to be one that is enabled, such as a text
// handler on io.Discard.
func Logger(t testing.TB, inner slog.Handler) (*slog.Logger, *Recorder) {
	t.Helper()
	r := &Recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var ev Event
		if err := json.Unmarshal(body, &ev); err == nil {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	r.client = observability.NewBugBarnClient(observability.BugBarnConfig{
		Endpoint: srv.URL, APIKey: "test-key", Project: "spanbarn", Environment: "test", Version: "dev",
	})
	t.Cleanup(func() {
		r.shutdown()
		srv.Close()
	})
	return slog.New(observability.NewBugBarnHandler(inner, r.client)), r
}

func (r *Recorder) shutdown() { r.stop.Do(r.client.Shutdown) }

// Events stops the client, which sends what it still holds, and returns every
// event BugBarn received. Nothing logged after the first call is sent.
func (r *Recorder) Events() []Event {
	r.shutdown()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}
