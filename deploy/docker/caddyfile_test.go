package docker

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A deploy replaces the hashed chunks. A tab that still holds the old index
// asks for chunks that no longer exist, and the one reload StaleChunk allows
// must then fetch a fresh index. That needs three things from the web server:
// hashed assets are immutable, a missing asset is a 404 and not index.html,
// and everything else is revalidated.
func TestCaddyfileServesAssetsAndIndexSafely(t *testing.T) {
	raw, err := os.ReadFile("Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	assets := block(t, src, "handle /assets/*")
	if !strings.Contains(assets, "immutable") {
		t.Errorf("/assets/* must be cached as immutable:\n%s", assets)
	}
	if strings.Contains(assets, "try_files") {
		t.Errorf("/assets/* must not fall back to index.html, a removed chunk has to be a 404:\n%s", assets)
	}

	fallback := block(t, src, "handle")
	if !strings.Contains(fallback, `Cache-Control "no-cache"`) {
		t.Errorf("the index fallback must send Cache-Control: no-cache:\n%s", fallback)
	}
	if !strings.Contains(fallback, "try_files {path} /index.html") {
		t.Errorf("client routes must fall back to index.html:\n%s", fallback)
	}
}

// block returns the body of the first top-level handler whose header line is
// exactly header followed by " {".
func block(t *testing.T, src, header string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\t` + regexp.QuoteMeta(header) + ` \{\n((?:\t\t.*\n|\n)*?)\t\}`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no %q block in Caddyfile", header)
	}
	return m[1]
}
