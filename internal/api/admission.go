package api

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DiskProbe reports how full the volume holding the database is, on a 0..1
// scale. ok is false when the figure could not be measured, in which case the
// caller must treat the system as healthy rather than guess.
type DiskProbe func(ctx context.Context) (used float64, ok bool)

// Admission decides whether the service should still accept telemetry.
//
// It is the last rung of an escalation ladder that starts in the retention
// worker: at 75% full retention halves its raw-telemetry windows, at 90% it
// quarters them, and if the volume still climbs to the threshold here we stop
// accepting telemetry altogether.
//
// Shedding telemetry is the *point*, not a regrettable side effect. When SQLite
// hits SQLITE_FULL every write fails at once — including the web_sessions
// insert that logging in depends on — so an operator loses the dashboard at
// exactly the moment they need it, and the database cannot be recovered in
// place because freeing rows itself needs space to commit. Dropping spans is
// cheap and recoverable; losing the control plane is neither.
//
// The probe result is cached: admission is consulted on every ingest request
// and a syscall per request would be pointless when the figure moves over
// minutes.
type Admission struct {
	probe     DiskProbe
	threshold float64
	ttl       time.Duration

	mu       sync.Mutex
	checked  time.Time
	lastUsed float64
	lastOK   bool

	shed atomic.Int64
}

// NewAdmission builds a controller that refuses telemetry once the database
// volume is at or above threshold (0..1). A threshold outside (0,1) disables
// shedding entirely, which is the correct behaviour for a deployment that has
// not opted in.
func NewAdmission(probe DiskProbe, threshold float64) *Admission {
	return &Admission{probe: probe, threshold: threshold, ttl: 10 * time.Second}
}

// Enabled reports whether this controller can ever refuse anything.
func (a *Admission) Enabled() bool {
	return a != nil && a.probe != nil && a.threshold > 0 && a.threshold < 1
}

// Shed returns how many requests have been refused since start.
func (a *Admission) Shed() int64 {
	if a == nil {
		return 0
	}
	return a.shed.Load()
}

// used returns the cached volume-used fraction, refreshing it at most once per
// ttl. A probe that fails leaves the previous reading in place rather than
// flipping to "unknown", so a transient statfs error cannot re-open the gate
// during an actual disk-full event.
func (a *Admission) used(ctx context.Context) (float64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if time.Since(a.checked) < a.ttl {
		return a.lastUsed, a.lastOK
	}
	if u, ok := a.probe(ctx); ok {
		a.lastUsed, a.lastOK = u, true
	}
	a.checked = time.Now()
	return a.lastUsed, a.lastOK
}

// Accepting reports whether telemetry should be admitted right now, along with
// the volume-used fraction behind the decision. An unmeasurable volume always
// admits: the guard must fail open, since a broken probe taking ingest down
// would be a worse outage than the one it prevents.
func (a *Admission) Accepting(ctx context.Context) (bool, float64) {
	if !a.Enabled() {
		return true, 0
	}
	used, ok := a.used(ctx)
	if !ok {
		return true, 0
	}
	return used < a.threshold, used
}

// Middleware refuses telemetry requests with 503 while the volume is over the
// threshold. 503 (not 429) is deliberate: this is a server-side capacity
// problem, not the client exceeding a quota, and OTLP exporters treat 503 with
// Retry-After as a retryable backoff rather than a reason to drop their queue.
func (a *Admission) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !a.Enabled() {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, _ := a.Accepting(r.Context()); !ok {
				a.shed.Add(1)
				w.Header().Set("Retry-After", "30")
				writeError(w, http.StatusServiceUnavailable,
					"telemetry temporarily refused: storage volume is nearly full", "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UnaryInterceptor is the gRPC equivalent of Middleware. It exists separately
// because the OTLP gRPC surface does not pass through the HTTP mux at all — a
// middleware-only gate would leave that path wide open, which is exactly the
// kind of half-applied guard that reads as protection while doing nothing.
func (a *Admission) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if ok, _ := a.Accepting(ctx); !ok {
			a.shed.Add(1)
			return nil, status.Error(codes.Unavailable,
				"telemetry temporarily refused: storage volume is nearly full")
		}
		return handler(ctx, req)
	}
}
