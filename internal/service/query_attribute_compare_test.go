package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/filter"
	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func setScan(scanned int64, keys map[string]map[string]int64) *repository.AttributeSetScan {
	s := &repository.AttributeSetScan{Scanned: scanned, Keys: map[string]*repository.AttributeSetValues{}}
	for k, counts := range keys {
		s.Keys[k] = &repository.AttributeSetValues{Counts: counts}
	}
	return s
}

func TestRankAttributesByTotalVariationDistance(t *testing.T) {
	sel := setScan(10, map[string]map[string]int64{
		"ua":     {"Mozilla": 10},
		"region": {"eu": 5, "us": 5},
		"same":   {"x": 10},
		"rare":   {"a": 2},
	})
	base := setScan(100, map[string]map[string]int64{
		"ua":     {"curl": 90, "Mozilla": 10},
		"region": {"eu": 80, "us": 20},
		"same":   {"x": 100},
		"rare":   {"a": 100},
	})
	got := rankAttributes(sel, base, 10, 5)

	if len(got) != 3 {
		t.Fatalf("keys with no difference must be dropped: %+v", got)
	}
	if got[0].Key != "ua" || math.Abs(got[0].Score-0.9) > 1e-9 {
		t.Errorf("top = %s %.3f, want ua 0.900", got[0].Key, got[0].Score)
	}
	if got[0].Values[0].Value != "curl" || got[0].Values[0].SelectionShare != 0 || got[0].Values[0].BaselineShare != 0.9 {
		t.Errorf("top value = %+v", got[0].Values[0])
	}
	// rare: selection sets it on 20% of spans, baseline on all. The missing
	// bucket carries the difference.
	if got[1].Key != "rare" || math.Abs(got[1].Score-0.8) > 1e-9 || !got[1].Values[1].Missing {
		t.Errorf("rare = %+v", got[1])
	}
	if got[2].Key != "region" || got[2].Score >= got[1].Score {
		t.Errorf("order: %+v", got)
	}
}

func TestRankAttributesLimitsAndEmptySets(t *testing.T) {
	sel := setScan(4, map[string]map[string]int64{"a": {"1": 4}, "b": {"1": 4}})
	base := setScan(4, map[string]map[string]int64{"a": {"2": 2, "3": 2}, "b": {"2": 4}})
	got := rankAttributes(sel, base, 1, 1)
	if len(got) != 1 || len(got[0].Values) != 1 {
		t.Fatalf("limits not applied: %+v", got)
	}
	if got[0].Key != "a" && got[0].Key != "b" {
		t.Errorf("tie broke on %s", got[0].Key)
	}
	if out := rankAttributes(setScan(0, nil), base, 5, 5); len(out) != 0 || out == nil {
		t.Errorf("an empty selection ranks nothing, got %v", out)
	}
}

func TestRankAttributesCountsTheLumpedRemainder(t *testing.T) {
	sel := &repository.AttributeSetScan{Scanned: 10, Keys: map[string]*repository.AttributeSetValues{
		"id": {Counts: map[string]int64{"a": 1}, Other: 9},
	}}
	base := &repository.AttributeSetScan{Scanned: 10, Keys: map[string]*repository.AttributeSetValues{
		"id": {Counts: map[string]int64{"a": 1}, Other: 4},
	}}
	got := rankAttributes(sel, base, 5, 5)
	// 4 spans of baseline have no id: |0.9-0.4| + missing |0-0.5| over 2.
	if len(got) != 1 || math.Abs(got[0].Score-0.5) > 1e-9 {
		t.Fatalf("got %+v", got)
	}
}

func compareQuery() AttributeCompareQuery {
	now := time.Now().UTC()
	return AttributeCompareQuery{
		ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour),
		Selection: &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "name", Op: filter.OpEq, Value: "POST /orphan"}}},
	}
}

func TestCompareAttributesValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	noSelection := compareQuery()
	noSelection.Selection = nil
	emptySelection := compareQuery()
	emptySelection.Selection = &filter.Expr{Match: "and"}
	tooWide := compareQuery()
	tooWide.From = now.Add(-8 * 24 * time.Hour)
	noProject := compareQuery()
	noProject.ProjectID = 0

	for name, q := range map[string]AttributeCompareQuery{
		"no selection": noSelection, "empty selection": emptySelection, "too wide": tooWide, "no project": noProject,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.CompareAttributes(context.Background(), q); !errors.Is(err, ErrInvalidAttributeRequest) {
				t.Errorf("err = %v", err)
			}
		})
	}

	bad := compareQuery()
	bad.Baseline = &filter.Expr{Match: "and", Filters: []filter.Node{{Key: "x", Op: "nope"}}}
	if _, err := svc.CompareAttributes(context.Background(), bad); !errors.Is(err, filter.ErrInvalid) {
		t.Errorf("bad baseline err = %v", err)
	}
}

func TestCompareAttributesSampleDefaults(t *testing.T) {
	q := compareQuery()
	if compareSample(q) != 1 {
		t.Error("an hour reads every span")
	}
	q.From = q.To.Add(-48 * time.Hour)
	if compareSample(q) != autoSampleRatio {
		t.Error("two days samples")
	}
	q.Sample = 3
	if compareSample(q) != 3 {
		t.Error("explicit ratio wins")
	}
}

// The orphan-span scenario of the issue: the selection is browser traffic, the
// baseline every span, and the user agent must rank first.
func TestCompareAttributesFindsTheDifferingAttribute(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	var spans []repository.Span
	add := func(i int, name, attrs string) {
		s := healthSpan(fmt.Sprint("t", i), fmt.Sprint("s", i), "", name)
		s.Attributes = attrs
		spans = append(spans, s)
	}
	for i := 0; i < 5; i++ {
		add(i, "POST /orphan", `{"user_agent.original":"Mozilla","http.response.status_code":200}`)
	}
	for i := 5; i < 30; i++ {
		add(i, "GET /api", `{"user_agent.original":"curl","http.response.status_code":200}`)
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}

	res, err := svc.CompareAttributes(context.Background(), compareQuery())
	if err != nil {
		t.Fatal(err)
	}
	if res.Selection.Scanned != 5 || res.Baseline.Scanned != 30 || res.Sample != 1 || res.Selection.Truncated {
		t.Fatalf("meta: %+v", res)
	}
	var keys []string
	for _, a := range res.Attributes {
		keys = append(keys, a.Key)
		if a.Key == "http.response.status_code" {
			t.Errorf("identical attribute was ranked")
		}
	}
	top := res.Attributes[0]
	if top.Key == "" || (top.Key != "name" && top.Key != "user_agent.original") {
		t.Fatalf("ranking: %v", keys)
	}
	var ua *AttributeDifference
	for i := range res.Attributes {
		if res.Attributes[i].Key == "user_agent.original" {
			ua = &res.Attributes[i]
		}
	}
	if ua == nil || ua.Values[0].Value != "curl" && ua.Values[0].Value != "Mozilla" {
		t.Fatalf("user agent missing: %v", keys)
	}
	if want := 25.0 / 30; math.Abs(ua.Score-want) > 1e-9 {
		t.Errorf("user agent score = %f, want %f", ua.Score, want)
	}
}

func TestCompareAttributesTruncationFlag(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	var spans []repository.Span
	for i := 0; i < 6; i++ {
		spans = append(spans, healthSpan(fmt.Sprint("t", i), fmt.Sprint("s", i), "", "POST /orphan"))
	}
	if err := repo.InsertSpans(spans); err != nil {
		t.Fatal(err)
	}
	q := compareQuery()
	q.MaxSpans = 3
	res, err := svc.CompareAttributes(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Selection.Truncated || res.Selection.Scanned != 3 || res.MaxSpans != 3 {
		t.Errorf("meta: %+v", res)
	}
}
