package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The four cases every ratchet gate in the estate has to prove it handles:
// violation present, violation baselined, baseline beatable, clean tree.
// A gate nobody has watched fail is a gate nobody knows works.

const violatingFile = `package api

import (
	"fmt"

	"github.com/wiebe-xyz/spanbarn/internal/repository"
)

var _ = fmt.Sprint
var _ = repository.New
`

const cleanFile = `package api

import "fmt"

var _ = fmt.Sprint
`

// mentionsButDoesNotImport proves the check reads imports rather than grepping:
// the denied path appears in a comment and a string literal.
const mentionsButDoesNotImport = `package api

// See github.com/wiebe-xyz/spanbarn/internal/repository for the query layer.
const doc = "github.com/wiebe-xyz/spanbarn/internal/repository"
`

func fixture(t *testing.T, files map[string]string, baseline string) (root, baselinePath string) {
	t.Helper()
	root = t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	baselinePath = filepath.Join(root, "baseline.txt")
	if err := os.WriteFile(baselinePath, []byte(baseline), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, baselinePath
}

func runCheck(t *testing.T, root, baselinePath string, extra ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	args := append([]string{"-root", root, "-baseline", baselinePath}, extra...)
	code := run(args, &out, &out)
	return code, out.String()
}

func TestViolationPresentFails(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x.go": violatingFile}, "")
	code, out := runCheck(t, root, bl)
	if code != 1 {
		t.Fatalf("want exit 1, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "NEW   internal/api/x.go") {
		t.Fatalf("want the new violation named, got:\n%s", out)
	}
}

func TestViolationBaselinedPasses(t *testing.T) {
	root, bl := fixture(t,
		map[string]string{"internal/api/x.go": violatingFile},
		"internal/api/x.go\tgithub.com/wiebe-xyz/spanbarn/internal/repository\n")
	code, out := runCheck(t, root, bl)
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out)
	}
}

func TestBaselineBeatableFails(t *testing.T) {
	// The file was fixed but the baseline still lists it. Ratchet property 3:
	// a stale entry silently re-opens room for the same violation.
	root, bl := fixture(t,
		map[string]string{"internal/api/x.go": cleanFile},
		"internal/api/x.go\tgithub.com/wiebe-xyz/spanbarn/internal/repository\n")
	code, out := runCheck(t, root, bl)
	if code != 1 {
		t.Fatalf("want exit 1 for a stale baseline, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "STALE") {
		t.Fatalf("want STALE in the output, got:\n%s", out)
	}
	if !strings.Contains(out, "-update") {
		t.Fatalf("want the refresh command named, got:\n%s", out)
	}
}

func TestCleanTreePasses(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x.go": cleanFile}, "")
	code, out := runCheck(t, root, bl)
	if code != 0 {
		t.Fatalf("want exit 0, got %d\n%s", code, out)
	}
}

func TestImportsAreParsedNotGrepped(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x.go": mentionsButDoesNotImport}, "")
	code, out := runCheck(t, root, bl)
	if code != 0 {
		t.Fatalf("a comment and a string literal are not imports; got %d\n%s", code, out)
	}
}

func TestTestFilesAreExempt(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x_test.go": violatingFile}, "")
	if code, out := runCheck(t, root, bl); code != 0 {
		t.Fatalf("test files are exempt; got %d\n%s", code, out)
	}
}

func TestReportOnlyNeverBlocks(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x.go": violatingFile}, "")
	code, out := runCheck(t, root, bl, "-report-only")
	if code != 0 {
		t.Fatalf("soak mode must not block, got %d\n%s", code, out)
	}
	if !strings.Contains(out, "NEW") || !strings.Contains(out, "soak") {
		t.Fatalf("soak mode must still report, got:\n%s", out)
	}
}

func TestMissingBaselineIsAHardError(t *testing.T) {
	root, _ := fixture(t, map[string]string{"internal/api/x.go": violatingFile}, "")
	code, out := runCheck(t, root, filepath.Join(root, "does-not-exist.txt"))
	if code != 2 {
		t.Fatalf("a missing baseline must not read as clean; got %d\n%s", code, out)
	}
}

func TestUpdateWritesTheBaseline(t *testing.T) {
	root, bl := fixture(t, map[string]string{"internal/api/x.go": violatingFile}, "")
	if code, out := runCheck(t, root, bl, "-update"); code != 0 {
		t.Fatalf("update failed: %d\n%s", code, out)
	}
	if code, out := runCheck(t, root, bl); code != 0 {
		t.Fatalf("after -update the tree must match the baseline; got %d\n%s", code, out)
	}
}
