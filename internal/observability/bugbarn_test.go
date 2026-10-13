package observability

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func captureOne(t *testing.T, env string, attrs map[string]any) bugbarnEvent {
	t.Helper()
	got := make(chan bugbarnEvent, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var ev bugbarnEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			t.Errorf("decode event: %v", err)
		}
		got <- ev
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := NewBugBarnClient(BugBarnConfig{Endpoint: srv.URL, APIKey: "k", Project: "spanbarn", Environment: env})
	c.CaptureLog("ERROR", "boom", attrs)
	c.Shutdown()
	return <-got
}

func TestBugBarnEventsCarryTheEnvironmentAsAttributes(t *testing.T) {
	ev := captureOne(t, "production", map[string]any{"route": "/x"})

	for _, k := range []string{"environment", "deployment.environment"} {
		if ev.Attributes[k] != "production" {
			t.Errorf("attributes[%q] = %v, want production", k, ev.Attributes[k])
		}
	}
	if ev.Attributes["route"] != "/x" {
		t.Errorf("caller attribute lost: %v", ev.Attributes)
	}
	if ev.Tags["environment"] != "production" {
		t.Errorf("tag environment = %q", ev.Tags["environment"])
	}
}

func TestBugBarnEnvironmentDoesNotOverrideACallerValueOrMutateItsMap(t *testing.T) {
	in := map[string]any{"environment": "canary"}
	ev := captureOne(t, "staging", in)

	if ev.Attributes["environment"] != "canary" {
		t.Errorf("caller value overridden: %v", ev.Attributes["environment"])
	}
	if ev.Attributes["deployment.environment"] != "staging" {
		t.Errorf("deployment.environment = %v", ev.Attributes["deployment.environment"])
	}
	if _, added := in["deployment.environment"]; added {
		t.Error("the caller's map was mutated")
	}
}

func TestBugBarnWithoutEnvironmentAddsNothing(t *testing.T) {
	ev := captureOne(t, "", nil)
	if len(ev.Attributes) != 0 {
		t.Errorf("attributes = %v, want none", ev.Attributes)
	}
}
