package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A workload that satisfies every assertion.
const cleanDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: spanbarn
spec:
  template:
    spec:
      containers:
      - name: spanbarn
        image: ghcr.io/wiebe-xyz/spanbarn/service:abc123
        readinessProbe:
          httpGet: {path: /healthz, port: 8080}
        livenessProbe:
          httpGet: {path: /healthz, port: 8080}
        resources:
          requests: {cpu: 100m, memory: 128Mi}
          limits: {cpu: "1", memory: 512Mi}
        env:
        - name: SPANBARN_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: spanbarn-secrets
              key: admin-password
`

// Same workload with the liveness probe removed.
var missingProbe = strings.Replace(cleanDeployment, `        livenessProbe:
          httpGet: {path: /healthz, port: 8080}
`, "", 1)

// renderDir writes the same manifest as every overlay, since the assertions run
// per overlay and the test only cares about the ratchet semantics.
func renderDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	for _, overlay := range overlays {
		path := filepath.Join(dir, overlay+".yaml")
		if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func baselineFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "infra-baseline.txt")
	body := "# test baseline\n" + strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCheck(t *testing.T, rendered, baseline string, extra ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"-rendered", rendered, "-baseline", baseline}, extra...)
	return run(args, &out, &out), out.String()
}

// allOverlays expands one finding line into the per-overlay lines the check
// emits, because every overlay renders the same fixture.
func allOverlays(check, object string) []string {
	var out []string
	for _, overlay := range overlays {
		out = append(out, fmt.Sprintf("%s\t%s\t%s", overlay, check, object))
	}
	return out
}

func TestViolationPresentFails(t *testing.T) {
	code, out := runCheck(t, renderDir(t, missingProbe), baselineFile(t))
	if code != 1 {
		t.Fatalf("want exit 1, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "NEW   [testing] no-liveness-probe") {
		t.Fatalf("want the finding named, got:\n%s", out)
	}
}

func TestViolationBaselinedPasses(t *testing.T) {
	bl := baselineFile(t, allOverlays("no-liveness-probe", "Deployment/spanbarn:spanbarn")...)
	code, out := runCheck(t, renderDir(t, missingProbe), bl)
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out)
	}
}

func TestBaselineBeatableFails(t *testing.T) {
	// The probe was added but the baseline still excuses its absence.
	bl := baselineFile(t, allOverlays("no-liveness-probe", "Deployment/spanbarn:spanbarn")...)
	code, out := runCheck(t, renderDir(t, cleanDeployment), bl)
	if code != 1 {
		t.Fatalf("want exit 1 for a stale baseline, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "STALE") || !strings.Contains(out, "-update") {
		t.Fatalf("want STALE and the refresh command, got:\n%s", out)
	}
}

func TestCleanTreePasses(t *testing.T) {
	code, out := runCheck(t, renderDir(t, cleanDeployment), baselineFile(t))
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out)
	}
}

func TestReportOnlyNeverBlocks(t *testing.T) {
	code, out := runCheck(t, renderDir(t, missingProbe), baselineFile(t), "-report-only")
	if code != 0 {
		t.Fatalf("soak mode must not block, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "NEW") || !strings.Contains(out, "soak") {
		t.Fatalf("soak mode must still report, got:\n%s", out)
	}
}

func TestUnrenderableOverlayIsAHardError(t *testing.T) {
	// "Did not run" must never be indistinguishable from "clean".
	dir := t.TempDir()
	code, out := runCheck(t, dir, baselineFile(t))
	if code != 2 {
		t.Fatalf("a missing render must exit 2, got %d\n%s", code, out)
	}
}

func TestEmptyRenderIsAHardError(t *testing.T) {
	dir := t.TempDir()
	for _, overlay := range overlays {
		if err := os.WriteFile(filepath.Join(dir, overlay+".yaml"), []byte("---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, out := runCheck(t, dir, baselineFile(t))
	if code != 2 {
		t.Fatalf("an overlay that renders nothing must exit 2, got %d\n%s", code, out)
	}
}

func TestAssertions(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"floating tag", strings.Replace(cleanDeployment,
			"image: ghcr.io/wiebe-xyz/spanbarn/service:abc123",
			"image: ghcr.io/wiebe-xyz/spanbarn/service:latest", 1), "floating-image-tag"},
		{"untagged image", strings.Replace(cleanDeployment,
			"image: ghcr.io/wiebe-xyz/spanbarn/service:abc123",
			"image: ghcr.io/wiebe-xyz/spanbarn/service", 1), "floating-image-tag"},
		{"no requests", strings.Replace(cleanDeployment,
			"          requests: {cpu: 100m, memory: 128Mi}\n", "", 1), "no-resource-requests"},
		{"no limits", strings.Replace(cleanDeployment,
			`          limits: {cpu: "1", memory: 512Mi}`+"\n", "", 1), "no-resource-limits"},
		{"no readiness", strings.Replace(cleanDeployment,
			"        readinessProbe:\n          httpGet: {path: /healthz, port: 8080}\n", "", 1), "no-readiness-probe"},
		{"undeclared secret", strings.Replace(cleanDeployment,
			"              name: spanbarn-secrets", "              name: nobody-applies-this", 1), "undeclared-secret-ref"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runCheck(t, renderDir(t, tc.manifest), baselineFile(t))
			if code != 1 {
				t.Fatalf("want exit 1, got %d\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("want %q in the output, got:\n%s", tc.want, out)
			}
		})
	}
}

func TestDigestPinnedImageIsNotFloating(t *testing.T) {
	m := strings.Replace(cleanDeployment,
		"image: ghcr.io/wiebe-xyz/spanbarn/service:abc123",
		"image: ghcr.io/wiebe-xyz/spanbarn/service@sha256:deadbeef", 1)
	if code, out := runCheck(t, renderDir(t, m), baselineFile(t)); code != 0 {
		t.Fatalf("a digest-pinned image is not floating; got %d\n%s", code, out)
	}
}

func TestSecretDeclaredInTheRenderIsNotAFinding(t *testing.T) {
	m := strings.Replace(cleanDeployment, "              name: spanbarn-secrets",
		"              name: in-tree-secret", 1) + `---
apiVersion: v1
kind: Secret
metadata:
  name: in-tree-secret
`
	if code, out := runCheck(t, renderDir(t, m), baselineFile(t)); code != 0 {
		t.Fatalf("a Secret in the render satisfies the reference; got %d\n%s", code, out)
	}
}
