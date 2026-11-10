package main

import (
	"go/ast"
	"testing"
)

// TestEveryModeThatStartsSpanRollupsFlushesThemOnStop pins the shutdown half of
// the span rollup wiring. SpanRollups.Run returns on cancel without a last
// flush, and the rollups sit in memory until the next tick, so a mode that
// starts them has to call flushSpanRollupsOnStop once its worker has stopped
// feeding them. Writer mode did from #485; standalone mode started the same
// rollups and lost up to one flush interval of rows at every stop.
//
// Like the sampler test, this asserts on source structure: the run* functions
// build their dependencies inline and block on Serve, so a test cannot
// construct one.
func TestEveryModeThatStartsSpanRollupsFlushesThemOnStop(t *testing.T) {
	files := parsePackage(t)

	checked := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name == "newSpanRollups" {
				continue
			}
			if !bodyCalls(fn, "newSpanRollups") {
				continue
			}
			checked++
			if !bodyCalls(fn, "flushSpanRollupsOnStop") {
				t.Errorf("%s starts span rollups with newSpanRollups but never calls flushSpanRollupsOnStop: "+
					"the rollups held in memory at shutdown are lost", fn.Name.Name)
			}
		}
	}
	if checked < 2 {
		t.Fatalf("found %d functions calling newSpanRollups, want at least 2 (writer and standalone); "+
			"if the wiring moved behind a helper, update this test", checked)
	}
}
