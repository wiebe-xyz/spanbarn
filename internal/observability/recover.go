package observability

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
)

// defaultClient is the BugBarn client installed by SetupWithConfig. Recovery
// paths use it to report panics as exceptions; it is nil when BugBarn is not
// configured.
var defaultClient atomic.Pointer[BugBarnClient]

// PanicError wraps a recovered panic value so it can be captured as an
// exception with the stack of the panic site.
type PanicError struct {
	Value any
	Stack string
}

func (p *PanicError) Error() string { return fmt.Sprintf("panic: %v", p.Value) }

// ReportPanic logs a recovered panic and, when BugBarn is configured, sends it
// as an exception. With flush set the event is delivered before returning,
// for paths that are about to terminate the process.
func ReportPanic(where string, recovered any, flush bool) {
	stack := string(debug.Stack())
	slog.Error("panic recovered", "where", where, "panic", fmt.Sprint(recovered), "stack", stack)
	c := defaultClient.Load()
	if c == nil {
		return
	}
	c.CaptureError(&PanicError{Value: recovered, Stack: stack}, map[string]any{
		"where": where,
		"stack": stack,
	})
	if flush {
		c.Flush()
	}
}

// RecoverAndReport is for `defer observability.RecoverAndReport("main")` at the
// top of a goroutine or main. It reports the panic synchronously and re-panics
// when repanic is set so the process still dies loudly.
func RecoverAndReport(where string, repanic bool) {
	if r := recover(); r != nil {
		ReportPanic(where, r, true)
		if repanic {
			panic(r)
		}
	}
}

// SafeGo runs fn in a goroutine that reports a panic instead of killing the
// process silently. It does not restart fn; callers that must keep a subsystem
// alive should loop around it.
func SafeGo(name string, wg *sync.WaitGroup, fn func()) {
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			if r := recover(); r != nil {
				ReportPanic("goroutine:"+name, r, false)
			}
		}()
		fn()
	}()
}
