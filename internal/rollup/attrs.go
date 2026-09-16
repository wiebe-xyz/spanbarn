package rollup

import (
	"strings"

	"github.com/wiebe-xyz/spanbarn/internal/metrics"
)

// DropPolicy lists the attribute keys removed when a tier is built. Keys ending
// in "*" match by prefix.
//
// Dropping an attribute merges every series that differed only in it, which is
// where most of the saving comes from: production carried 10,483 attribute sets
// for one metric, largely because service.version and service.instance.id change
// on every deploy and server.address varies per tenant host. An hour of data
// does not need to distinguish those; a month certainly does not.
type DropPolicy struct {
	// Hourly is removed from the hourly tier and everything coarser.
	Hourly []string
	// Daily is removed from the daily tier and coarser, on top of Hourly.
	Daily []string
}

// DefaultDropPolicy is the built-in reduction. Everything it drops identifies a
// process, a host or an unbounded path; nothing it drops distinguishes one
// operation from another.
//
// Deliberately kept: service.name, deployment.environment, http.route, the
// request method and the status code — the labels a chart is actually grouped by.
func DefaultDropPolicy() DropPolicy {
	return DropPolicy{
		Hourly: []string{
			"service.version",
			"service.instance.id",
			"server.port",
			"net.host.port",
			"net.host.name",
			"host.name",
			"k8s.pod.name",
			"process.pid",
			"session.id",
			"telemetry.sdk.*",
			"network.protocol.*",
			"url.scheme",
			"http.scheme",
			"http.flavor",
		},
		Daily: []string{
			// Raw request targets. These are unbounded by nature (ids in the
			// path), which is exactly what a long-range chart must not key on.
			"path",
			"url.path",
			"url.full",
			"http.url",
			"http.target",
			// Host identity: useful for a day, noise for a year.
			"server.address",
			"http.host",
			"http.server_name",
		},
	}
}

// keysFor returns the keys dropped when building a tier of the given step.
func (p DropPolicy) keysFor(step int64) []string {
	switch {
	case step >= StepDay:
		return append(append([]string(nil), p.Hourly...), p.Daily...)
	case step >= StepHour:
		return p.Hourly
	default:
		return nil
	}
}

// Reduce strips the tier's dropped keys from a label set and returns the
// canonical JSON and fingerprint of what is left. Both come from
// internal/metrics, so a reduced series is keyed exactly like any other.
func (p DropPolicy) Reduce(attributes string, step int64) (string, string) {
	attrs := metrics.ParseAttributes([]byte(attributes))
	for _, pattern := range p.keysFor(step) {
		dropMatching(attrs, pattern)
	}
	return metrics.CanonicalAttributes(attrs), metrics.Fingerprint(attrs)
}

// dropMatching removes one key, or every key under a "prefix.*" pattern.
func dropMatching(attrs map[string]string, pattern string) {
	if !strings.HasSuffix(pattern, "*") {
		delete(attrs, pattern)
		return
	}
	prefix := strings.TrimSuffix(pattern, "*")
	for k := range attrs {
		if strings.HasPrefix(k, prefix) {
			delete(attrs, k)
		}
	}
}

// ParseDropList reads a comma-separated setting value into a key list. Empty
// entries are skipped so a trailing comma is harmless.
func ParseDropList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}
