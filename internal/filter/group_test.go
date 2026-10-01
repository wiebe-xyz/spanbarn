package filter

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	if err := ValidateKey("url.path"); err != nil {
		t.Errorf("url.path: %v", err)
	}
	for _, bad := range []string{"", `a"b`, `a\b`, strings.Repeat("k", maxKeyLen+1)} {
		if err := ValidateKey(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v, want ErrInvalid", bad, err)
		}
	}
}

func TestTextSQLReadsColumnsAndAttributes(t *testing.T) {
	if sql, args := TextSQL("service"); sql != "service" || len(args) != 0 {
		t.Errorf("column = %q %v", sql, args)
	}
	if sql, args := TextSQL("duration_us"); !strings.Contains(sql, "CAST(duration_us") || len(args) != 0 {
		t.Errorf("int column = %q %v", sql, args)
	}
	if sql, args := TextSQL("url.path"); !strings.Contains(sql, "json_extract") || len(args) != 2 || args[0] != `$."url.path"` {
		t.Errorf("attribute = %q %v", sql, args)
	}
}

func TestGroupCondition(t *testing.T) {
	if c := GroupCondition("a", ""); c.Op != OpNotExists {
		t.Errorf("empty value = %+v", c)
	}
	if c := GroupCondition("a", "x"); c.Op != OpEq || c.Value != "x" {
		t.Errorf("value = %+v", c)
	}
}

func TestConjoin(t *testing.T) {
	conds := []Node{{Key: "g", Op: OpEq, Value: "1"}}
	got, ok := Conjoin(nil, conds)
	if !ok || got.Match != MatchAnd || len(got.Filters) != 1 {
		t.Fatalf("nil base = %+v %v", got, ok)
	}

	and := &Expr{Match: MatchAnd, Filters: []Node{{Key: "a", Op: OpExists}, {Match: MatchOr, Filters: []Node{{Key: "b", Op: OpExists}}}}}
	got, ok = Conjoin(and, conds)
	if !ok || len(got.Filters) != 3 || got.Validate() != nil {
		t.Fatalf("and base = %+v %v", got, ok)
	}

	or := &Expr{Match: MatchOr, Filters: []Node{{Key: "a", Op: OpExists}, {Key: "b", Op: OpExists}}}
	got, ok = Conjoin(or, conds)
	if !ok || got.Match != MatchAnd || got.Filters[0].Match != MatchOr || got.Validate() != nil {
		t.Fatalf("or base = %+v %v", got, ok)
	}

	nested := &Expr{Match: MatchOr, Filters: []Node{{Key: "a", Op: OpExists}, {Match: MatchAnd, Filters: []Node{{Key: "b", Op: OpExists}}}}}
	if _, ok := Conjoin(nested, conds); ok {
		t.Error("an OR over a group cannot take another level")
	}
}
