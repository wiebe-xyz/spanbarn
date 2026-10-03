package slo

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBudgetRemaining(t *testing.T) {
	cases := []struct {
		name        string
		target      float64
		good, total int64
		want        float64
	}{
		{"zero traffic", 0.995, 0, 0, 1},
		{"no bad events", 0.995, 1000, 1000, 1},
		{"sixty percent spent", 0.995, 997, 1000, 0.4},
		{"exactly spent", 0.995, 995, 1000, 0},
		{"overspent", 0.995, 980, 1000, -3},
		{"target 0.9", 0.9, 90, 100, 0},
	}
	for _, c := range cases {
		if got := BudgetRemaining(c.target, c.good, c.total); !near(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestBurnRate(t *testing.T) {
	cases := []struct {
		name        string
		target      float64
		good, total int64
		want        float64
	}{
		{"zero traffic", 0.995, 0, 0, 0},
		{"no bad events", 0.995, 1000, 1000, 0},
		{"on pace", 0.995, 995, 1000, 1},
		{"above 1", 0.995, 950, 1000, 10},
		{"below 1", 0.995, 998, 1000, 0.4},
	}
	for _, c := range cases {
		if got := BurnRate(c.target, c.good, c.total); !near(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestDegenerateTarget(t *testing.T) {
	if got := BurnRate(1, 9, 10); got != 0 {
		t.Errorf("burn with target 1 = %v", got)
	}
	if got := BudgetRemaining(1, 10, 10); got != 1 {
		t.Errorf("budget with target 1, no bad = %v", got)
	}
	if got := BudgetRemaining(1, 9, 10); got != -1 {
		t.Errorf("budget with target 1, bad = %v", got)
	}
}
