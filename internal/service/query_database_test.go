package service

import (
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/cache"
)

func TestParseAttrs(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		if got := parseAttrs(""); got != nil {
			t.Fatalf("expected nil for empty input, got %v", got)
		}
	})
	t.Run("empty object literal", func(t *testing.T) {
		if got := parseAttrs("{}"); got != nil {
			t.Fatalf("expected nil for {}, got %v", got)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		if got := parseAttrs("not-json"); got != nil {
			t.Fatalf("expected nil for malformed JSON, got %v", got)
		}
	})
	t.Run("valid json", func(t *testing.T) {
		got := parseAttrs(`{"db.system":"sqlite","db.name":"main"}`)
		if got == nil {
			t.Fatal("expected map, got nil")
		}
		if got["db.system"] != "sqlite" || got["db.name"] != "main" {
			t.Fatalf("unexpected map contents: %v", got)
		}
	})
}

func TestExtractSQLOperation(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM users":        "SELECT",
		"  INSERT INTO foo (x) ":     "INSERT",
		"DELETE":                     "DELETE",
		"UPDATE foo SET x=1":         "UPDATE",
		"":                           "",
		"   ":                        "",
		"WITH cte AS (...) SELECT *": "WITH",
	}
	for input, want := range cases {
		if got := extractSQLOperation(input); got != want {
			t.Errorf("extractSQLOperation(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQueryServiceCacheAccessors(t *testing.T) {
	repo := setupTestRepo(t)
	svc := NewQueryService(repo, nil, nil)

	if svc.Cache() != nil {
		t.Fatalf("Cache() should start nil, got %v", svc.Cache())
	}

	c := cache.New(cache.NewMemoryStore(), time.Minute)
	svc.SetCache(c)
	if svc.Cache() != c {
		t.Fatalf("SetCache did not propagate to Cache()")
	}
}
