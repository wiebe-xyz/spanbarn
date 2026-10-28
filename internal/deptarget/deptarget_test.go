package deptarget

import (
	"slices"
	"testing"
)

func TestExtract(t *testing.T) {
	tests := []struct {
		name       string
		attrs      string
		wantTarget string
		wantType   string
	}{
		{"empty", "", "", ""},
		{"empty object", "{}", "", ""},
		{"invalid json", "{", "", ""},
		{"db system", `{"db.system":"sqlite"}`, "sqlite", "database"},
		{"db system beats name", `{"db.system":"sqlite","db.name":"app"}`, "sqlite", "database"},
		{"db name", `{"db.name":"app"}`, "app", "database"},
		{"peer service", `{"peer.service":"billing"}`, "billing", "service"},
		{"rpc", `{"rpc.service":"Greeter"}`, "Greeter", "rpc"},
		{"messaging", `{"messaging.system":"kafka"}`, "kafka", "messaging"},
		{"aws", `{"aws.service":"S3"}`, "S3", "aws"},
		{"http url host", `{"http.url":"https://api.example.com/v1/x"}`, "api.example.com", "http"},
		{"url full host", `{"url.full":"https://other.example.com/y"}`, "other.example.com", "http"},
		{"url without host falls through", `{"http.url":"/relative","http.host":"h"}`, "h", "http"},
		{"http host", `{"http.host":"h.example.com"}`, "h.example.com", "http"},
		{"server address", `{"server.address":"10.0.0.1"}`, "10.0.0.1", "network"},
		{"net peer name", `{"net.peer.name":"peer"}`, "peer", "network"},
		{"non-string value", `{"db.system":7}`, "", ""},
		{"nothing matches", `{"other":"x"}`, "", ""},
	}
	for _, tc := range tests {
		target, typ := Extract(tc.attrs)
		if target != tc.wantTarget || typ != tc.wantType {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, target, typ, tc.wantTarget, tc.wantType)
		}
	}
}

type spanList []Span

func (l spanList) Len() int      { return len(l) }
func (l spanList) At(i int) Span { return l[i] }

func TestCalls(t *testing.T) {
	spans := spanList{
		{TraceID: "t1", SpanID: "a", Service: "web", Kind: "server"},
		{TraceID: "t1", SpanID: "b", ParentSpanID: "a", Service: "web", Kind: "CLIENT", Attributes: `{"db.system":"sqlite"}`},
		{TraceID: "t1", SpanID: "c", ParentSpanID: "a", Service: "api", Kind: "server"},
		{TraceID: "t1", SpanID: "d", ParentSpanID: "a", Service: "web", Kind: "internal"},
		{TraceID: "t1", SpanID: "e", ParentSpanID: "missing", Service: "api"},
		// Same span id in another trace: its parent is not t1's "a".
		{TraceID: "t2", SpanID: "f", ParentSpanID: "a", Service: "billing"},
		{TraceID: "t3", SpanID: "g", Kind: "client", Attributes: `{"peer.service":"auth"}`},
	}
	var got []Call
	Calls(spans, func(c Call) { got = append(got, c) })
	want := []Call{
		{Span: 1, Source: "web", Target: "sqlite", TargetType: "database"},
		{Span: 6, Source: "", Target: "auth", TargetType: TypeService},
		{Span: 0, Source: "web", Target: "api", TargetType: TypeService},
	}
	if !slices.Equal(got, want) {
		t.Errorf("calls = %+v\nwant    %+v", got, want)
	}
}
