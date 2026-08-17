package repository

import (
	"os"
	"syscall"
	"testing"
)

// diskBlocks returns the number of 512-byte blocks actually allocated to path.
// Used to distinguish a real reservation from a sparse file.
func diskBlocks(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform; cannot check sparseness")
	}
	return int64(st.Blocks)
}
