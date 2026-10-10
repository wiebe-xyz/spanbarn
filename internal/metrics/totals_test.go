package metrics

import "testing"

func totalOf(ts []Total, key, val string) (float64, bool) {
	for _, t := range ts {
		if t.Labels[key] == val {
			return t.Total, true
		}
	}
	return 0, false
}

func TestTotalsDeltaAndCumulativeAgree(t *testing.T) {
	delta := []InputPoint{
		{T: 10e9, Value: 100, Delta: true, Attributes: map[string]string{"session.id": "a"}},
		{T: 20e9, Value: 150, Delta: true, Attributes: map[string]string{"session.id": "a"}},
	}
	cumulative := []InputPoint{
		{T: 0, Value: 0, Attributes: map[string]string{"session.id": "b"}},
		{T: 10e9, Value: 100, Attributes: map[string]string{"session.id": "b"}},
		{T: 20e9, Value: 250, Attributes: map[string]string{"session.id": "b"}},
	}
	got := Totals("sum", append(delta, cumulative...), []string{"session.id"})
	a, _ := totalOf(got, "session.id", "a")
	b, _ := totalOf(got, "session.id", "b")
	if a != 250 || b != 250 {
		t.Fatalf("a=%v b=%v, want 250 each", a, b)
	}
}

func TestTotalsCumulativeResetAndSort(t *testing.T) {
	pts := []InputPoint{
		{T: 0, Value: 300, Attributes: map[string]string{"s": "x", "host": "h1"}},
		{T: 10e9, Value: 50, Attributes: map[string]string{"s": "x", "host": "h1"}}, // reset: +50
		{T: 0, Value: 1, Attributes: map[string]string{"s": "y", "host": "h1"}},
		{T: 10e9, Value: 1000, Attributes: map[string]string{"s": "y", "host": "h1"}}, // +999
	}
	// Two series share host=h1; grouping by host sums their increases.
	got := Totals("sum", pts, []string{"host"})
	if len(got) != 1 || got[0].Total != 1049 {
		t.Fatalf("got %+v, want one group of 1049", got)
	}
	// Grouping by s keeps them apart, largest first.
	got = Totals("sum", pts, []string{"s"})
	if len(got) != 2 || got[0].Labels["s"] != "y" || got[1].Total != 50 {
		t.Fatalf("got %+v", got)
	}
}

func TestTotalsIgnoresNonSums(t *testing.T) {
	if got := Totals("gauge", []InputPoint{{T: 1, Value: 5}}, nil); got != nil {
		t.Fatalf("gauge gave %+v", got)
	}
}
