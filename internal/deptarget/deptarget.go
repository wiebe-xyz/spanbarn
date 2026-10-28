// Package deptarget finds the calls a set of spans shows: a client span
// calling a dependency named in its attributes, and a span whose parent
// belongs to another service. The service map and dependency reads use it on
// stored spans, and the span_edges writer on every span it receives, so both
// count the same calls.
package deptarget

import (
	"encoding/json"
	"net/url"
)

// TypeService is the target type of a call into another instrumented service.
const TypeService = "service"

// IsClientKind reports whether a span kind is the client kind in either casing.
func IsClientKind(kind string) bool {
	return kind == "client" || kind == "CLIENT"
}

// rule maps a span attribute to a dependency target type. A hostOnly rule
// reads the host out of a URL and is skipped when there is none.
type rule struct {
	key        string
	targetType string
	hostOnly   bool
}

// rules lists the attributes that name a dependency, most specific first.
var rules = []rule{
	{"db.system", "database", false},
	{"db.name", "database", false},
	{"peer.service", TypeService, false},
	{"rpc.service", "rpc", false},
	{"messaging.system", "messaging", false},
	{"aws.service", "aws", false},
	{"http.url", "http", true},
	{"url.full", "http", true},
	{"http.host", "http", false},
	{"server.address", "network", false},
	{"net.peer.name", "network", false},
}

// Extract returns the dependency a client span's attributes JSON names, or
// empty strings when none does.
func Extract(attrJSON string) (target, targetType string) {
	if attrJSON == "" || attrJSON == "{}" {
		return "", ""
	}
	var attrs map[string]any
	if err := json.Unmarshal([]byte(attrJSON), &attrs); err != nil {
		return "", ""
	}
	for _, r := range rules {
		v, ok := StringAttr(attrs, r.key)
		if !ok {
			continue
		}
		if !r.hostOnly {
			return v, r.targetType
		}
		if host := Host(v); host != "" {
			return host, r.targetType
		}
	}
	return "", ""
}

// StringAttr returns a non-empty string attribute.
func StringAttr(attrs map[string]any, key string) (string, bool) {
	v, ok := attrs[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// Host returns the host of a URL, or "" when it does not parse.
func Host(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// Span is the part of a span the call rules read.
type Span struct {
	TraceID, SpanID, ParentSpanID string
	Service, Kind, Attributes     string
}

// Spans is a set of spans Calls walks.
type Spans interface {
	Len() int
	At(i int) Span
}

// Call is one call Source made to Target. Span is the index of the span whose
// duration and status the call carries: the client span, or the parent of a
// span in another service.
type Call struct {
	Span                       int
	Source, Target, TargetType string
}

// Calls emits every call the spans show. A client span that names a
// dependency calls it; its Source is empty when the span names no service.
// A span whose parent is in the set and belongs to another service is a call
// from the parent's service, carried by the parent.
func Calls(spans Spans, emit func(Call)) {
	n := spans.Len()
	type spanKey struct{ trace, span string }
	byID := make(map[spanKey]int, n)
	for i := 0; i < n; i++ {
		sp := spans.At(i)
		byID[spanKey{sp.TraceID, sp.SpanID}] = i
		if !IsClientKind(sp.Kind) {
			continue
		}
		if target, targetType := Extract(sp.Attributes); target != "" {
			emit(Call{Span: i, Source: sp.Service, Target: target, TargetType: targetType})
		}
	}
	for i := 0; i < n; i++ {
		sp := spans.At(i)
		if sp.ParentSpanID == "" || sp.Service == "" {
			continue
		}
		p, ok := byID[spanKey{sp.TraceID, sp.ParentSpanID}]
		if !ok {
			continue
		}
		parent := spans.At(p)
		if parent.Service == "" || parent.Service == sp.Service {
			continue
		}
		emit(Call{Span: p, Source: parent.Service, Target: sp.Service, TargetType: TypeService})
	}
}
