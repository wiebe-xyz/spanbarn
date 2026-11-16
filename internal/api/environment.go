package api

import (
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Environment attribute names. SpanBarn stores and filters on
// deployment.environment; the OpenTelemetry semantic conventions renamed it to
// deployment.environment.name, and newer SDKs send only the new name.
const (
	envAttr     = "deployment.environment"
	envAttrName = "deployment.environment.name"
)

// resolveResource makes sure res carries deployment.environment and returns
// the resource to use (a new one when res was nil and a key environment
// applies). Order: the resource's own deployment.environment, then its
// deployment.environment.name, then the authenticating key's environment.
// A resource with none of the three is left as it is (#558).
func resolveResource(res *resourcepb.Resource, keyEnv string) *resourcepb.Resource {
	var named string
	for _, kv := range res.GetAttributes() {
		switch kv.GetKey() {
		case envAttr:
			if kv.GetValue().GetStringValue() != "" {
				return res
			}
		case envAttrName:
			named = kv.GetValue().GetStringValue()
		}
	}
	env := named
	if env == "" {
		env = keyEnv
	}
	if env == "" {
		return res
	}
	if res == nil {
		res = &resourcepb.Resource{}
	}
	res.Attributes = append(res.Attributes, &commonpb.KeyValue{
		Key:   envAttr,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: env}},
	})
	return res
}

// stampTraceEnvironment applies resolveResource to every resource of req.
func stampTraceEnvironment(req *collectorpb.ExportTraceServiceRequest, keyEnv string) {
	for _, rs := range req.GetResourceSpans() {
		rs.Resource = resolveResource(rs.GetResource(), keyEnv)
	}
}

// stampLogEnvironment applies resolveResource to every resource of req.
func stampLogEnvironment(req *collectorlogspb.ExportLogsServiceRequest, keyEnv string) {
	for _, rl := range req.GetResourceLogs() {
		rl.Resource = resolveResource(rl.GetResource(), keyEnv)
	}
}

// stampMetricEnvironment applies resolveResource to every resource of req.
func stampMetricEnvironment(req *collectormetricspb.ExportMetricsServiceRequest, keyEnv string) {
	for _, rm := range req.GetResourceMetrics() {
		rm.Resource = resolveResource(rm.GetResource(), keyEnv)
	}
}
