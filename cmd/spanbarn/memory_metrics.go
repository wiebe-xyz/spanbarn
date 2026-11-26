package main

import (
	"bufio"
	"os"
	"runtime/metrics"
	"strconv"
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/selfmetrics"
)

const cgroupMemoryDir = "/sys/fs/cgroup"

// goMemoryMetrics maps the Go runtime's memory classes to the self-metric that
// reports them.
var goMemoryMetrics = []struct{ metric, name string }{
	{"/memory/classes/total:bytes", "spanbarn.mem.go_total_bytes"},
	{"/memory/classes/heap/objects:bytes", "spanbarn.mem.go_heap_objects_bytes"},
	{"/memory/classes/heap/free:bytes", "spanbarn.mem.go_heap_free_bytes"},
	{"/memory/classes/heap/released:bytes", "spanbarn.mem.go_heap_released_bytes"},
}

// cgroupMemoryFields are the memory.stat lines exported next to the Go classes.
// The kernel's anon figure minus the Go total is what the process holds outside
// the Go runtime (SQLite page caches allocated through libc), and file is the
// page cache the container is charged for.
var cgroupMemoryFields = []struct{ field, name string }{
	{"anon", "spanbarn.mem.cgroup_anon_bytes"},
	{"file", "spanbarn.mem.cgroup_file_bytes"},
	{"file_mapped", "spanbarn.mem.cgroup_file_mapped_bytes"},
}

// registerMemoryGauges exports where the pod's memory sits: the Go runtime's
// classes, and the container's anon and file-cache split from its cgroup. The
// cgroup gauges are absent outside a cgroup v2 container.
func registerMemoryGauges(rec *selfmetrics.Recorder) {
	if rec == nil {
		return
	}
	for _, m := range goMemoryMetrics {
		metric := m.metric
		rec.RegisterGauge(m.name, nil, func() float64 { return readGoMemory(metric) })
	}
	for _, f := range cgroupMemoryFields {
		field := f.field
		rec.RegisterOptionalGauge(f.name, nil, func() (float64, bool) {
			return readCgroupMemory(cgroupMemoryDir, field)
		})
	}
	rec.RegisterOptionalGauge("spanbarn.mem.cgroup_current_bytes", nil, func() (float64, bool) {
		return readCgroupFile(cgroupMemoryDir + "/memory.current")
	})
	rec.RegisterOptionalGauge("spanbarn.mem.cgroup_limit_bytes", nil, func() (float64, bool) {
		return readCgroupFile(cgroupMemoryDir + "/memory.max")
	})
	registerCacheCounters(rec, cgroupMemoryDir)
	rec.RegisterOptionalGauge("spanbarn.io.pressure_some_pct", nil, func() (float64, bool) {
		return readPressureAvg10(procPressureIO)
	})
}

const procPressureIO = "/proc/pressure/io"

// cacheCounters are cumulative memory.stat counts. File refaults are pages the
// container evicted from its file cache and then read from disk again: a
// squeezed page cache shows there (about 5000 a second on 2026-11-26, when
// every raw-span read hit its 10 s budget) well before reads fail.
var cacheCounters = []struct{ field, name string }{
	{"workingset_refault_file", "spanbarn.mem.file_refaults"},
	{"pgmajfault", "spanbarn.mem.major_faults"},
}

// registerCacheCounters exports the cacheCounters the cgroup reports. A host
// without cgroup v2 memory.stat exports none, instead of a flat zero.
func registerCacheCounters(rec *selfmetrics.Recorder, dir string) {
	for _, c := range cacheCounters {
		field := c.field
		if _, ok := readCgroupMemory(dir, field); !ok {
			continue
		}
		rec.RegisterCounter(c.name, nil, func() float64 {
			v, _ := readCgroupMemory(dir, field)
			return v
		})
	}
}

// readPressureAvg10 returns the 10 s average of the "some" line of a PSI file
// (the share of time at least one task waited), in percent.
func readPressureAvg10(path string) (float64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				n, err := strconv.ParseFloat(v, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

func readGoMemory(name string) float64 {
	s := []metrics.Sample{{Name: name}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return float64(s[0].Value.Uint64())
}

// readCgroupMemory returns one field of dir/memory.stat.
func readCgroupMemory(dir, field string) (float64, bool) {
	f, err := os.Open(dir + "/memory.stat")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, val, ok := strings.Cut(sc.Text(), " ")
		if !ok || name != field {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		return float64(n), err == nil
	}
	return 0, false
}

func readCgroupFile(path string) (float64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return float64(n), err == nil
}
