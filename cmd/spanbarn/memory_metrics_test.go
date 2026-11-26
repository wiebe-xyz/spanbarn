package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

func TestReadCgroupMemoryPicksTheNamedField(t *testing.T) {
	dir := t.TempDir()
	stat := "anon 1386557440\nfile 737091584\nfile_mapped 148201472\nanon_thp 0\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]float64{"anon": 1386557440, "file": 737091584, "file_mapped": 148201472} {
		if got, ok := readCgroupMemory(dir, field); !ok || got != want {
			t.Errorf("%s = %v %v, want %v", field, got, ok, want)
		}
	}
	if _, ok := readCgroupMemory(dir, "slab"); ok {
		t.Error("a field the file lacks must read as absent")
	}
	if _, ok := readCgroupMemory(t.TempDir(), "anon"); ok {
		t.Error("a missing memory.stat must read as absent")
	}
}

func TestReadCgroupFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "memory.current")
	if err := os.WriteFile(p, []byte("2168926208\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readCgroupFile(p); !ok || got != 2168926208 {
		t.Errorf("memory.current = %v %v", got, ok)
	}
	if err := os.WriteFile(p, []byte("max\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readCgroupFile(p); ok {
		t.Error("a non-numeric value must read as absent")
	}
	if _, ok := readCgroupFile(p + ".none"); ok {
		t.Error("a missing file must read as absent")
	}
}

func TestMemoryGaugesReportGoClasses(t *testing.T) {
	rec := selfmetrics.NewRecorder()
	registerMemoryGauges(rec)
	registerMemoryGauges(nil)
	got := map[string]float64{}
	for _, r := range rec.Readings() {
		got[r.Name] = r.Value
	}
	if got["spanbarn.mem.go_total_bytes"] <= 0 || got["spanbarn.mem.go_heap_objects_bytes"] <= 0 {
		t.Errorf("go memory classes = %v", got)
	}
}

func TestReadPressureAvg10(t *testing.T) {
	p := filepath.Join(t.TempDir(), "io")
	psi := "some avg10=67.34 avg60=62.77 avg300=61.20 total=704129913667\nfull avg10=37.66 avg60=33.79 avg300=35.87 total=436634964989\n"
	if err := os.WriteFile(p, []byte(psi), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readPressureAvg10(p); !ok || got != 67.34 {
		t.Errorf("avg10 = %v %v, want the some line's 67.34", got, ok)
	}
	if _, ok := readPressureAvg10(p + ".none"); ok {
		t.Error("a host without PSI must read as absent")
	}
}

func TestCacheCountersOnlyWhereTheCgroupReportsThem(t *testing.T) {
	dir := t.TempDir()
	stat := "anon 1\nworkingset_refault_file 9000000\npgmajfault 142000\n"
	if err := os.WriteFile(filepath.Join(dir, "memory.stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := selfmetrics.NewRecorder()
	registerCacheCounters(rec, dir)
	got := map[string]float64{}
	for _, r := range rec.Readings() {
		got[r.Name] = r.Value
	}
	if got["spanbarn.mem.file_refaults"] != 9000000 || got["spanbarn.mem.major_faults"] != 142000 {
		t.Errorf("cache counters = %v", got)
	}

	bare := selfmetrics.NewRecorder()
	registerCacheCounters(bare, t.TempDir())
	if n := len(bare.Readings()); n != 0 {
		t.Errorf("%d counters exported without a memory.stat, want none", n)
	}
}
