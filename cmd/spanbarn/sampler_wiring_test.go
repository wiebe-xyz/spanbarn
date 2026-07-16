package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// otlpServingModes are the run modes that accept OTLP spans. Each must route
// them through the TraceBuffer, which is where ratio sampling happens.
// runWriterMode is absent deliberately: it passes a nil ingest handler and
// serves no OTLP.
var otlpServingModes = []string{"runStandalone", "runReaderMode", "runIngestMode"}

// TestEveryOTLPModeWiresTheSampler pins the wiring that nothing else checks.
//
// Sampling lives in ingest.TraceBuffer, and a mode opts into it by passing
// api.WithTraceBuffer. A mode that forgets still compiles, still serves, still
// passes every other test — it just silently ingests everything unsampled and
// ignores every ingest.sample_ratio.* setting. runStandalone shipped that way
// and runIngestMode never had it at all; nobody noticed until production's disk
// filled with SpanBarn's own telemetry.
//
// This asserts on source structure rather than behaviour because the run*
// functions build their dependencies inline and then block on Serve, so there is
// no seam to construct one from a test. If you refactor the wiring (e.g. behind
// a shared helper), update samplerWiringCall below — do not delete the test.
func TestEveryOTLPModeWiresTheSampler(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	const samplerWiringCall = "WithTraceBuffer"

	for _, mode := range otlpServingModes {
		fn := findFunc(file, mode)
		if fn == nil {
			t.Fatalf("run mode %s not found in main.go — if it was renamed or removed, update otlpServingModes", mode)
		}
		if !bodyCalls(fn, samplerWiringCall) {
			t.Errorf("%s serves OTLP but never calls api.%s — spans will be ingested unsampled "+
				"and every ingest.sample_ratio.* setting will be silently ignored", mode, samplerWiringCall)
		}
	}
}

// TestWriterModeServesNoOTLP guards the assumption that lets us exclude the
// writer from the sampling requirement above: it passes a nil ingest handler, so
// there is nothing to sample. If that changes, the writer needs a buffer too.
func TestWriterModeServesNoOTLP(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	fn := findFunc(file, "runWriterMode")
	if fn == nil {
		t.Skip("runWriterMode not found")
	}
	if bodyCalls(fn, "NewTraceBuffer") {
		t.Skip("runWriterMode now builds a trace buffer; it presumably serves OTLP — nothing to guard")
	}

	// It must still pass a nil ingest handler to the server constructor.
	var passesNilIngest bool
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !calleeName(call, "NewServerWithQuery") {
			return true
		}
		// signature: NewServerWithQuery(cfg, ingestHandler, querySvc, sessions, logger, opts...)
		if len(call.Args) >= 2 {
			if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "nil" {
				passesNilIngest = true
			}
		}
		return true
	})
	if !passesNilIngest {
		t.Error("runWriterMode now passes a non-nil ingest handler: it accepts spans, so it must " +
			"also wire api.WithTraceBuffer — add it to otlpServingModes")
	}
}

func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Recv == nil {
			return fn
		}
	}
	return nil
}

// bodyCalls reports whether fn's body contains a call whose callee name matches.
func bodyCalls(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && calleeName(call, name) {
			found = true
			return false
		}
		return true
	})
	return found
}

// calleeName matches both `name(...)` and `pkg.name(...)`.
func calleeName(call *ast.CallExpr, name string) bool {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == name
	case *ast.SelectorExpr:
		return fn.Sel.Name == name
	}
	return strings.HasSuffix(exprString(call.Fun), "."+name)
}

func exprString(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
