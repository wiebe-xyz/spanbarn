package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

func attributeQuery() AttributeQuery {
	now := time.Now().UTC()
	return AttributeQuery{ProjectID: 1, From: now.Add(-time.Hour), To: now.Add(time.Hour)}
}

func TestAttributeQueryValidation(t *testing.T) {
	svc := NewQueryService(setupTestRepo(t), nil, nil)
	now := time.Now().UTC()
	cases := map[string]AttributeQuery{
		"missing project": {From: now.Add(-time.Hour), To: now},
		"missing range":   {ProjectID: 1},
		"inverted":        {ProjectID: 1, From: now, To: now.Add(-time.Hour)},
		"too wide":        {ProjectID: 1, From: now.Add(-8 * 24 * time.Hour), To: now},
		"negative sample": {ProjectID: 1, From: now.Add(-time.Hour), To: now, Sample: -1},
		"huge sample":     {ProjectID: 1, From: now.Add(-time.Hour), To: now, Sample: 5000},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.DiscoverAttributes(context.Background(), q); !errors.Is(err, ErrInvalidAttributeRequest) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestAttributeSampleDefaultsByWindow(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name   string
		window time.Duration
		sample int
		want   int
	}{
		{"1h reads everything", time.Hour, 0, 1},
		{"24h reads everything", 24 * time.Hour, 0, 1},
		{"48h samples", 48 * time.Hour, 0, 20},
		{"7d samples", 7 * 24 * time.Hour, 0, 20},
		{"explicit ratio wins", 7 * 24 * time.Hour, 1, 1},
		{"explicit ratio on short window", time.Hour, 5, 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := AttributeQuery{ProjectID: 1, From: now.Add(-c.window), To: now, Sample: c.sample}.window()
			if err != nil || w.Sample != c.want {
				t.Errorf("sample = %d, %v; want %d", w.Sample, err, c.want)
			}
		})
	}
}

func TestAttributeCapsAreClamped(t *testing.T) {
	q := attributeQuery()
	q.MaxSpans, q.Limit, q.Top = 10_000_000, 10_000, 10_000
	w, err := q.window()
	if err != nil {
		t.Fatal(err)
	}
	if w.MaxSpans != hardAttributeMaxSpans || w.MaxKeys != hardAttributeKeys || w.TopValues != hardAttributeTop {
		t.Errorf("caps not clamped: %+v", w)
	}
	q.Key = "client.address"
	if w, _ = q.window(); w.TopValues != hardAttributeKeyTop {
		t.Errorf("single key top = %d", w.TopValues)
	}
	q = attributeQuery()
	if w, _ = q.window(); w.MaxSpans != defaultAttributeMaxSpans || w.MaxKeys != defaultAttributeKeys || w.TopValues != defaultAttributeTop {
		t.Errorf("defaults: %+v", w)
	}
}

func TestDiscoverAttributes(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)
	mk := func(id, name, attrs string) repository.Span {
		s := healthSpan(id, id, "", name)
		s.Attributes = attrs
		return s
	}
	if err := repo.InsertSpans([]repository.Span{
		mk("a", "POST /presign", `{"client.address":"1.1.1.1","http.response.status_code":200}`),
		mk("b", "POST /presign", `{"client.address":"1.1.1.1","http.response.status_code":500}`),
		mk("c", "POST /presign", `{"http.response.status_code":200}`),
		mk("d", "GET /other", `{"other":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}

	q := attributeQuery()
	q.SpanName = "POST /presign"
	res, err := svc.DiscoverAttributes(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 3 || res.Sample != 1 || res.Truncated {
		t.Fatalf("scan meta: %+v", res)
	}
	if len(res.Keys) != 2 || res.Keys[0].Key != "http.response.status_code" || res.Keys[0].Coverage != 1 {
		t.Fatalf("keys: %+v", res.Keys)
	}
	addr := res.Keys[1]
	if addr.Key != "client.address" || addr.Spans != 2 || addr.Distinct != 1 ||
		addr.Coverage < 0.66 || addr.Coverage > 0.67 || addr.Top[0].Value != "1.1.1.1" || addr.Top[0].Count != 2 {
		t.Errorf("client.address = %+v", addr)
	}

	q.MaxSpans = 2
	res, err = svc.DiscoverAttributes(context.Background(), q)
	if err != nil || !res.Truncated || res.Scanned != 2 {
		t.Errorf("row cap: %+v, %v", res, err)
	}

	empty := attributeQuery()
	empty.SpanName = "nope"
	res, err = svc.DiscoverAttributes(context.Background(), empty)
	if err != nil || res.Scanned != 0 || res.Keys == nil || len(res.Keys) != 0 {
		t.Errorf("empty window must return an empty key list: %+v, %v", res, err)
	}
}
