package main

import "testing"

func TestRuntimeVersion(t *testing.T) {
	t.Setenv("SPANBARN_VERSION", "")
	if got := runtimeVersion("abc123"); got != "abc123" {
		t.Fatalf("without the env var the build default wins, got %q", got)
	}
	t.Setenv("SPANBARN_VERSION", "v1.4.2")
	if got := runtimeVersion("abc123"); got != "v1.4.2" {
		t.Fatalf("the deploy's value wins, got %q", got)
	}
}
