package main

import "testing"

func TestWithPerUnitRatiosAndOrder(t *testing.T) {
	rows := []operationRow{
		{Operation: "page /app", Kind: "internal", SpanCount: 42000},
		{Operation: "GET /api/v1/auth/me", Kind: "client", SpanCount: 189000},
		{Operation: "GET /api/v1/runtime-config", Kind: "client", SpanCount: 84000},
	}
	got, err := withPerUnit(rows, "page /app")
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		op   string
		unit float64
	}{{"GET /api/v1/auth/me", 4.5}, {"GET /api/v1/runtime-config", 2}, {"page /app", 1}}
	for i, w := range want {
		if got[i].Operation != w.op || got[i].PerUnit == nil || *got[i].PerUnit != w.unit {
			t.Errorf("row %d = %s %v, want %s %v", i, got[i].Operation, got[i].PerUnit, w.op, w.unit)
		}
	}
	if rows[0].PerUnit != nil {
		t.Error("input rows were modified")
	}
}

func TestWithPerUnitNeedsTheBaseOperation(t *testing.T) {
	rows := []operationRow{{Operation: "GET /x", SpanCount: 5}}
	if _, err := withPerUnit(rows, "page /app"); err == nil {
		t.Fatal("want an error when the base operation is absent")
	}
	rows = append(rows, operationRow{Operation: "page /app", SpanCount: 0})
	if _, err := withPerUnit(rows, "page /app"); err == nil {
		t.Fatal("want an error when the base operation has no spans")
	}
}
