package aggregation

// rollupCaps tracks the services and span names admitted per project and
// hour, so one project that puts ids in its span names cannot grow
// span_rollups without bound. Hours reset with the process: after a restart
// an hour can admit up to twice the cap.
type rollupCaps struct {
	services map[capKey]map[string]struct{}
	names    map[capKey]map[string]struct{}
	// overflowed holds the (project, service, hour) keys that have already
	// been reported as over their cap.
	overflowed map[capKey]struct{}
}

type capKey struct {
	hour      int64
	projectID int64
	service   string // empty for the per-project service set
}

func newRollupCaps() rollupCaps {
	return rollupCaps{
		services:   map[capKey]map[string]struct{}{},
		names:      map[capKey]map[string]struct{}{},
		overflowed: map[capKey]struct{}{},
	}
}

// admit returns the service and name a span is recorded under, and whether
// this span is the first of its (project, service, hour) to hit a cap.
func (c *rollupCaps) admit(projectID int64, service, name string, bucket int64, cfg SpanRollupConfig) (string, string, bool) {
	hour := bucket - bucket%3600
	if !admitOne(c.services, capKey{hour: hour, projectID: projectID}, service, cfg.MaxServices) {
		return OtherName, OtherName, c.reportOnce(capKey{hour: hour, projectID: projectID})
	}
	if !admitOne(c.names, capKey{hour: hour, projectID: projectID, service: service}, name, cfg.MaxNames) {
		return service, OtherName, c.reportOnce(capKey{hour: hour, projectID: projectID, service: service})
	}
	return service, name, false
}

// admitOne reports whether v is, or can become, one of the at most limit
// values held for k.
func admitOne(sets map[capKey]map[string]struct{}, k capKey, v string, limit int) bool {
	set := sets[k]
	if set == nil {
		set = map[string]struct{}{}
		sets[k] = set
	}
	if _, ok := set[v]; ok {
		return true
	}
	if len(set) >= limit {
		return false
	}
	set[v] = struct{}{}
	return true
}

func (c *rollupCaps) reportOnce(k capKey) bool {
	if _, done := c.overflowed[k]; done {
		return false
	}
	c.overflowed[k] = struct{}{}
	return true
}

// prune forgets every hour before the previous one. A late span for such an
// hour starts a fresh set.
func (c *rollupCaps) prune(nowUnix int64) {
	keep := nowUnix - nowUnix%3600 - 3600
	for _, m := range []map[capKey]map[string]struct{}{c.services, c.names} {
		for k := range m {
			if k.hour < keep {
				delete(m, k)
			}
		}
	}
	for k := range c.overflowed {
		if k.hour < keep {
			delete(c.overflowed, k)
		}
	}
}
