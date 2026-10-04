package worker

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
	"github.com/wiebe-xyz/spanbarn/internal/sampling"
)

const hourUs = int64(time.Hour / time.Microsecond)

func cleanSpan(trace, op string, startUs int64) repository.Span {
	return repository.Span{ProjectID: 1, TraceID: trace, SpanID: trace + "-s", Name: op, Status: "ok", DurationUs: 100, StartTimeUs: startUs, IngestedAt: time.Unix(1000, 0)}
}

func durableWorker(policy *mockBoringPolicy, withHour bool) *RedisWorker {
	rw := &RedisWorker{
		cfg:          WorkerConfig{SlowThresholdUs: 1_000_000, BoringRetention: time.Hour},
		boringPolicy: policy,
		floor:        sampling.NewMinuteFloor(),
	}
	if withHour {
		rw.hourFloor = sampling.NewBucketFloor(time.Hour)
	}
	return rw
}

func byTrace(spans []repository.Span) map[string]repository.Span {
	m := map[string]repository.Span{}
	for _, s := range spans {
		m[s.TraceID] = s
	}
	return m
}

func TestClassifyDurableFirstCleanTracePerOpHour(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerMinute: 0, minPerHour: 1}, true)
	got := byTrace(rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-a", 1_000),
	}))
	if len(got) != 1 {
		t.Fatalf("want only the first trace kept, got %d", len(got))
	}
	s := got["t1"]
	if !s.Durable || s.ExpiresAt != nil {
		t.Fatalf("t1 want durable without expiry, got durable=%v expires=%v", s.Durable, s.ExpiresAt)
	}
}

func TestClassifyDurableSecondFallsBackToMinuteFloor(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerMinute: 1, minPerHour: 1}, true)
	got := byTrace(rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-a", 1_000),
		cleanSpan("t3", "op-a", 2_000),
	}))
	if !got["t1"].Durable || got["t1"].ExpiresAt != nil {
		t.Fatal("t1 should be durable and unstamped")
	}
	t2, ok := got["t2"]
	if !ok || t2.Durable || t2.ExpiresAt == nil {
		t.Fatalf("t2 want minute-floor keep with expiry, ok=%v durable=%v exp=%v", ok, t2.Durable, t2.ExpiresAt)
	}
	if _, ok := got["t3"]; ok {
		t.Fatal("t3 should be dropped once the minute floor is spent")
	}
}

func TestClassifyDurableDifferentOpSameHour(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerHour: 1}, true)
	got := byTrace(rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-b", 0),
	}))
	if !got["t1"].Durable || !got["t2"].Durable {
		t.Fatal("each op should get its own durable trace")
	}
}

func TestClassifyDurableNextHourAdmitsAgain(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerHour: 1}, true)
	got := byTrace(rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-a", hourUs+5),
	}))
	if !got["t1"].Durable || !got["t2"].Durable {
		t.Fatal("next hour should admit again")
	}
}

func TestClassifyDurableZeroDisables(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 1, minPerMinute: 0, minPerHour: 0}, true)
	got := rw.classifyForStorage([]repository.Span{cleanSpan("t1", "op-a", 0)})
	if len(got) != 1 || got[0].Durable || got[0].ExpiresAt == nil {
		t.Fatalf("tier disabled: want ordinary stamped boring trace, got %+v", got)
	}
}

func TestClassifyDurableErrorNeverDurable(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerHour: 1}, true)
	e := cleanSpan("t1", "op-a", 0)
	e.Status = "error"
	slow := cleanSpan("t2", "op-b", 0)
	slow.DurationUs = 5_000_000
	for _, s := range rw.classifyForStorage([]repository.Span{e, slow}) {
		if s.Durable {
			t.Fatalf("trace %s must not be durable", s.TraceID)
		}
	}
}

func TestClassifyDurableVerboseOnlyClean(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerHour: 1, verboseUntil: time.Now().Add(time.Hour)}, true)
	got := byTrace(rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-a", 1_000),
	}))
	if len(got) != 2 {
		t.Fatalf("verbose stores everything, got %d", len(got))
	}
	if !got["t1"].Durable || got["t2"].Durable {
		t.Fatalf("only the first verbose clean trace is durable: t1=%v t2=%v", got["t1"].Durable, got["t2"].Durable)
	}
	for _, s := range got {
		if s.ExpiresAt != nil {
			t.Fatal("verbose spans carry no stamp")
		}
	}
}

func TestClassifyDurableNilHourFloorUnchanged(t *testing.T) {
	rw := durableWorker(&mockBoringPolicy{ratio: 0, minPerMinute: 1, minPerHour: 1}, false)
	got := rw.classifyForStorage([]repository.Span{
		cleanSpan("t1", "op-a", 0),
		cleanSpan("t2", "op-a", 1_000),
	})
	if len(got) != 1 || got[0].Durable || got[0].ExpiresAt == nil {
		t.Fatalf("nil hourly floor: want one stamped non-durable span, got %+v", got)
	}
}
