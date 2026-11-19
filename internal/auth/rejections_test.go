package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type lookupFunc func(hash string) (APIKeyRecord, error)

func (f lookupFunc) GetAPIKeyByHash(h string) (APIKeyRecord, error) { return f(h) }
func (lookupFunc) TouchAPIKey(int64) error                          { return nil }

type recordingSink struct{ got []string }

func (r *recordingSink) Reject(fp string) { r.got = append(r.got, fp) }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAuthorizerReportsAKeyNoStoredKeyMatches(t *testing.T) {
	sink := &recordingSink{}
	a := NewAuthorizer("", lookupFunc(func(string) (APIKeyRecord, error) { return APIKeyRecord{}, ErrKeyNotFound }), discardLogger())
	a.SetRejectionSink(sink)

	if _, err := a.AuthorizeIdentity("a-revoked-key"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	want := HashKey("a-revoked-key")[:KeyFingerprintLen]
	if len(sink.got) != 1 || sink.got[0] != want {
		t.Errorf("reported = %v, want [%s]", sink.got, want)
	}
	if len(want) != 8 {
		t.Errorf("fingerprint %q has %d digits, want 8", want, len(want))
	}
}

func TestAuthorizerDoesNotReportWhatIsNoVerdictOnTheKey(t *testing.T) {
	sink := &recordingSink{}
	busy := lookupFunc(func(string) (APIKeyRecord, error) { return APIKeyRecord{}, errors.New("database is locked") })
	a := NewAuthorizer("", busy, discardLogger())
	a.SetRejectionSink(sink)
	if _, err := a.AuthorizeIdentity("some-key"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
	if _, err := a.AuthorizeIdentity("  "); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("empty key: err = %v, want unauthorized", err)
	}

	valid := NewAuthorizer("", lookupFunc(func(string) (APIKeyRecord, error) { return APIKeyRecord{ID: 4}, nil }), discardLogger())
	valid.SetRejectionSink(sink)
	if _, err := valid.AuthorizeIdentity("good"); err != nil {
		t.Fatal(err)
	}
	if len(sink.got) != 0 {
		t.Errorf("reported = %v, want nothing for a busy database, an empty key or a valid key", sink.got)
	}
}

func TestRejectionCounterTalliesPerFingerprintAndFlushes(t *testing.T) {
	c := NewRejectionCounter()
	now := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.Reject("aaaaaaaa")
	now = now.Add(time.Minute)
	c.Reject("aaaaaaaa")
	c.Reject("bbbbbbbb")
	c.Reject("")

	rows, dropped := c.Flush()
	if dropped != 0 || len(rows) != 2 {
		t.Fatalf("flush = %v dropped %d, want two fingerprints", rows, dropped)
	}
	got := map[string]KeyRejection{}
	for _, r := range rows {
		got[r.Fingerprint] = r
	}
	if a := got["aaaaaaaa"]; a.Count != 2 || a.LastSeen != now.Unix() {
		t.Errorf("aaaaaaaa = %+v, want 2 requests, newest at %d", a, now.Unix())
	}
	if rows, _ := c.Flush(); len(rows) != 0 {
		t.Errorf("second flush = %v, want empty", rows)
	}
}

func TestRejectionCounterBoundsDistinctKeys(t *testing.T) {
	c := NewRejectionCounter()
	for i := 0; i < rejectionMaxKeys+10; i++ {
		c.Reject(string(rune('a'+i%26)) + time.Duration(i).String())
	}
	rows, dropped := c.Flush()
	if len(rows) != rejectionMaxKeys || dropped != 10 {
		t.Errorf("rows %d dropped %d, want %d and 10", len(rows), dropped, rejectionMaxKeys)
	}
	var nilCounter *RejectionCounter
	nilCounter.Reject("aaaaaaaa") // a pod without a counter must not panic
}

func TestRejectionCounterKeepsCountsWhenTheStoreFails(t *testing.T) {
	c := NewRejectionCounter()
	c.Reject("aaaaaaaa")
	fail := true
	var stored []KeyRejection
	store := func(_ context.Context, rows []KeyRejection) error {
		if fail {
			return errors.New("redis down")
		}
		stored = append(stored, rows...)
		return nil
	}
	c.FlushTo(context.Background(), store, discardLogger())
	c.Reject("aaaaaaaa")
	fail = false
	c.FlushTo(context.Background(), store, discardLogger())
	if len(stored) != 1 || stored[0].Count != 2 {
		t.Errorf("stored = %v, want one row counting both requests", stored)
	}
}

func TestRejectionCounterRunFlushesOnItsInterval(t *testing.T) {
	c := NewRejectionCounter()
	c.Reject("aaaaaaaa")
	var mu sync.Mutex
	got := make(chan []KeyRejection, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx, 10*time.Millisecond, func(_ context.Context, rows []KeyRejection) error {
		mu.Lock()
		defer mu.Unlock()
		select {
		case got <- rows:
		default:
		}
		return nil
	}, discardLogger())
	select {
	case rows := <-got:
		if len(rows) != 1 || rows[0].Count != 1 {
			t.Errorf("rows = %v, want the one rejected request", rows)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no flush within 5 s")
	}
}
