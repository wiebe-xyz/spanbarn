package filter

import (
	"strings"
	"testing"
)

// The production node compiles to equality or absence on the environment
// attribute; any other name compiles to equality alone; "" and "all" add no
// condition.
func TestEnvironmentNodeCompiles(t *testing.T) {
	for _, env := range []string{"", "all"} {
		if _, ok := EnvironmentNode(env); ok {
			t.Errorf("EnvironmentNode(%q) returned a condition", env)
		}
	}

	prod, ok := EnvironmentNode(ProductionEnvironment)
	if !ok {
		t.Fatal("no node for production")
	}
	sqlProd, argsProd, err := Compile(&Expr{Match: MatchAnd, Filters: []Node{prod}})
	if err != nil {
		t.Fatalf("compile production: %v", err)
	}
	if !strings.Contains(sqlProd, " OR ") || len(argsProd) == 0 {
		t.Errorf("production SQL = %q args %v, want an OR over equality and absence", sqlProd, argsProd)
	}

	staging, _ := EnvironmentNode("staging")
	sqlStg, argsStg, err := Compile(&Expr{Match: MatchAnd, Filters: []Node{staging}})
	if err != nil {
		t.Fatalf("compile staging: %v", err)
	}
	if strings.Contains(sqlStg, " OR ") || len(argsStg) == 0 {
		t.Errorf("staging SQL = %q args %v, want a single equality", sqlStg, argsStg)
	}
}
