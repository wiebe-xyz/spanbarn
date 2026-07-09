package api

import "testing"

func TestSafeNextPath(t *testing.T) {
	safe := map[string]string{
		"/dashboard":       "/dashboard",
		"/a/b?x=1":         "/a/b?x=1",
		"/":                "/",
		"":                 "/",
		"//evil.com":       "/", // protocol-relative
		"https://evil.com": "/", // absolute URL
		"/\\evil.com":      "/", // backslash trick
		"javascript:alert": "/", // scheme, no leading slash
		"evil":             "/", // relative, no leading slash
	}
	for in, want := range safe {
		if got := safeNextPath(in); got != want {
			t.Errorf("safeNextPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSecretEqual(t *testing.T) {
	if !secretEqual("hunter2", "hunter2") {
		t.Error("equal secrets should compare equal")
	}
	if secretEqual("hunter2", "hunter3") {
		t.Error("different secrets must not compare equal")
	}
	if secretEqual("short", "longer-value") {
		t.Error("different-length secrets must not compare equal")
	}
	if secretEqual("", "x") {
		t.Error("empty vs non-empty must not compare equal")
	}
}
