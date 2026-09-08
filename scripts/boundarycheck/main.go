// Command boundarycheck enforces the layering of internal/ as a ratchet.
//
// internal/api is transport, internal/service is business logic and
// internal/repository is persistence. The intended dependency direction is
// api -> service -> repository. Today 14 files in internal/api reach straight
// past the service layer into the repository, which is why this ships as a
// soak: it runs, prints the count against a committed baseline, and does not
// block. Flip BOUNDARIES_MODE to `enforce` in scripts/quality-thresholds.conf
// to make it blocking.
//
// The ratchet has all four properties:
//  1. a violating file that is not in the baseline fails
//  2. per-file entries mean a file gaining a second denied import also fails
//  3. a baselined file that no longer violates fails as STALE, so the baseline
//     cannot rot into permanent permission
//  4. -update is the only escape hatch, and it rewrites a committed file
//
// Imports are read with go/parser rather than grep so that a path inside a
// comment or a string literal is not mistaken for a dependency.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const modulePath = "github.com/wiebe-xyz/spanbarn"

// rule denies importing Deny from anything under From.
type rule struct {
	From string
	Deny string
	Why  string
}

var rules = []rule{
	{
		From: "internal/api",
		Deny: "internal/repository",
		Why:  "transport must reach persistence through internal/service",
	},
	{
		From: "internal/repository",
		Deny: "internal/service",
		Why:  "persistence must not depend on business logic",
	},
	{
		From: "internal/repository",
		Deny: "internal/api",
		Why:  "persistence must not depend on transport",
	},
	{
		From: "internal/service",
		Deny: "internal/api",
		Why:  "business logic must not depend on transport",
	},
}

type violation struct {
	File   string // repo-relative path
	Import string // full import path
	Why    string
}

func (v violation) key() string { return v.File + "\t" + v.Import }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("boundarycheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", ".", "repository root to scan")
	baselinePath := flags.String("baseline", "", "baseline file (default <root>/scripts/boundaries-baseline.txt)")
	update := flags.Bool("update", false, "rewrite the baseline from the current tree")
	reportOnly := flags.Bool("report-only", false, "print findings but always exit 0 (soak mode)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *baselinePath == "" {
		*baselinePath = filepath.Join(*root, "scripts", "boundaries-baseline.txt")
	}

	found, err := scan(*root)
	if err != nil {
		fmt.Fprintf(stderr, "boundarycheck: %v\n", err)
		return 2
	}

	if *update {
		if err := writeBaseline(*baselinePath, found); err != nil {
			fmt.Fprintf(stderr, "boundarycheck: %v\n", err)
			return 2
		}
		fmt.Fprintf(stdout, "boundarycheck: baseline written with %d entr%s -> %s\n",
			len(found), pluralY(len(found)), *baselinePath)
		return 0
	}

	baseline, err := readBaseline(*baselinePath)
	if err != nil {
		fmt.Fprintf(stderr, "boundarycheck: %v\n", err)
		fmt.Fprintf(stderr, "create one with: go run ./scripts/boundarycheck -update\n")
		return 2
	}

	current := map[string]violation{}
	for _, v := range found {
		current[v.key()] = v
	}

	var added []violation
	for _, v := range found {
		if _, ok := baseline[v.key()]; !ok {
			added = append(added, v)
		}
	}
	var stale []string
	for k := range baseline {
		if _, ok := current[k]; !ok {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)

	fmt.Fprintf(stdout, "boundarycheck: %d boundary import%s across %d rule%s (baseline %d)\n",
		len(found), pluralS(len(found)), len(rules), pluralS(len(rules)), len(baseline))

	for _, v := range added {
		fmt.Fprintf(stdout, "  NEW   %s imports %s\n        %s\n", v.File, v.Import, v.Why)
	}
	for _, k := range stale {
		parts := strings.SplitN(k, "\t", 2)
		fmt.Fprintf(stdout, "  STALE %s no longer imports %s — remove it from the baseline\n", parts[0], parts[1])
	}

	if len(added) == 0 && len(stale) == 0 {
		fmt.Fprintf(stdout, "  matches the baseline exactly\n")
		return 0
	}
	fmt.Fprintf(stdout, "  refresh with: go run ./scripts/boundarycheck -update\n")
	if *reportOnly {
		fmt.Fprintf(stdout, "  (soak: not blocking — set BOUNDARIES_MODE=enforce to make it blocking)\n")
		return 0
	}
	return 1
}

// pluralY turns "entr" into "entry"/"entries"; pluralS turns "import" into
// "import"/"imports".
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// scan walks root and returns every denied import, sorted.
func scan(root string) ([]violation, error) {
	var out []violation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".cache", ".claude", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		matched := matchingRules(rel)
		if len(matched) == 0 {
			return nil
		}
		imports, err := fileImports(path)
		if err != nil {
			return err
		}
		for _, imp := range imports {
			for _, r := range matched {
				if imp == modulePath+"/"+r.Deny || strings.HasPrefix(imp, modulePath+"/"+r.Deny+"/") {
					out = append(out, violation{File: rel, Import: imp, Why: r.Why})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out, nil
}

func matchingRules(rel string) []rule {
	var out []rule
	for _, r := range rules {
		if strings.HasPrefix(rel, r.From+"/") {
			out = append(out, r)
		}
	}
	return out
}

func fileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	for _, spec := range f.Imports {
		out = append(out, strings.Trim(spec.Path.Value, `"`))
	}
	return out, nil
}

func writeBaseline(path string, vs []violation) error {
	var b strings.Builder
	b.WriteString("# boundarycheck baseline — files that import across a denied layer boundary.\n")
	b.WriteString("# Generated by: go run ./scripts/boundarycheck -update\n")
	b.WriteString("# Entries may only be removed. A line here that no longer violates fails the\n")
	b.WriteString("# check as STALE, so this file cannot rot into permanent permission.\n")
	for _, v := range vs {
		b.WriteString(v.File + "\t" + v.Import + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func readBaseline(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("baseline not readable: %w", err)
	}
	defer f.Close()
	out := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "\t") {
			return nil, fmt.Errorf("malformed baseline line: %q", line)
		}
		out[line] = struct{}{}
	}
	return out, sc.Err()
}
