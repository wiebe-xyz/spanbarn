package repository

import (
	"reflect"
	"testing"
	"time"
)

func TestWhereBuilder(t *testing.T) {
	b := &whereBuilder{}
	b.add("a = ?", 1)
	b.addNotIn("name", nil)
	b.addNotIn("name", []string{"x", "y"})
	from := time.Unix(10, 0)
	b.addTimeRange("t", from, time.Time{})
	b.addTimeRange("u", time.Time{}, from)

	wantWhere := []string{"a = ?", "name NOT IN (?,?)", "t >= ?", "u <= ?"}
	if !reflect.DeepEqual(b.where, wantWhere) {
		t.Fatalf("where = %v, want %v", b.where, wantWhere)
	}
	wantArgs := []any{1, "x", "y", from, from}
	if !reflect.DeepEqual(b.args, wantArgs) {
		t.Fatalf("args = %v, want %v", b.args, wantArgs)
	}
}

func TestRootSpanGroupWhere(t *testing.T) {
	where, args := SpanFilter{}.rootSpanGroupWhere()
	if len(where) != 1 || len(args) != 0 {
		t.Fatalf("empty filter: where=%v args=%v", where, args)
	}
	where, args = SpanFilter{ProjectID: 2, Service: "s", Status: "error", MinDuration: 5, ExcludeOperations: []string{"o"}}.rootSpanGroupWhere()
	if len(where) != 6 || len(args) != 5 {
		t.Fatalf("full filter: where=%v args=%v", where, args)
	}
}
