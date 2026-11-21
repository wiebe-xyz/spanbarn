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
