package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProject(t *testing.T) {
	cfg := Config{Project: "from-config"}

	// Flag wins over everything.
	if got := resolveProject("from-flag", cfg); got != "from-flag" {
		t.Errorf("flag should win, got %q", got)
	}

	// .spanbarn.json wins over config default.
	dir := t.TempDir()
	writeLocal(t, dir, LocalConfig{Project: "from-local"})
	withWd(t, dir, func() {
		if got := resolveProject("", cfg); got != "from-local" {
			t.Errorf("local config should win, got %q", got)
		}
	})

	// Falls back to config default when no flag/local.
	empty := t.TempDir()
	withWd(t, empty, func() {
		if got := resolveProject("", cfg); got != "from-config" {
			t.Errorf("config default expected, got %q", got)
		}
	})
}

func TestFindLocalConfigWalksUp(t *testing.T) {
	root := t.TempDir()
	writeLocal(t, root, LocalConfig{Project: "walkup"})
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	withWd(t, nested, func() {
		lc, ok := findLocalConfig()
		if !ok || lc.Project != "walkup" {
			t.Errorf("expected to find walkup config, got %+v ok=%v", lc, ok)
		}
	})
}

func TestOrderedKeys(t *testing.T) {
	obj := json.RawMessage(`{"traceId":"abc","durationUs":42,"nested":{"x":1},"status":"error"}`)
	keys := orderedKeys(obj)
	want := []string{"traceId", "durationUs", "nested", "status"}
	if len(keys) != len(want) {
		t.Fatalf("expected %d keys, got %v", len(want), keys)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("key %d: want %q got %q", i, want[i], keys[i])
		}
	}
}

func TestScalarString(t *testing.T) {
	cases := map[string]string{
		`"hello"`: "hello",
		`42`:      "42",
		`true`:    "true",
		`null`:    "",
		`{"a":1}`: `{"a":1}`,
		`[1,2,3]`: `[1,2,3]`,
	}
	for in, want := range cases {
		if got := scalarString(json.RawMessage(in)); got != want {
			t.Errorf("scalarString(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderTableNonArray(t *testing.T) {
	// Objects (not arrays) should not be treated as tables.
	if renderTable(json.RawMessage(`{"a":1}`)) {
		t.Error("expected renderTable to return false for an object")
	}
}

func writeLocal(t *testing.T, dir string, lc LocalConfig) {
	t.Helper()
	data, _ := json.Marshal(lc)
	if err := os.WriteFile(filepath.Join(dir, localConfigFile), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func withWd(t *testing.T, dir string, fn func()) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()
	fn()
}
