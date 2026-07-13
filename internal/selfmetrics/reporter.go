package selfmetrics

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// selfInstrumentUA marks self-generated ingest traffic so the server skips
// tracing it (matches the value used by self-tracing/self-logging).
const selfInstrumentUA = "spanbarn-self-instrument"

// Metric names emitted by the reporter.
const (
	metricRequests = "spanbarn.http.requests"
	metricDuration = "spanbarn.http.server.duration"
	metricRollups  = "spanbarn.rollups.persisted"
)

// Reporter periodically snapshots a Recorder and POSTs the values as OTLP
// metrics to SpanBarn's own /v1/metrics endpoint.
type Reporter struct {
	rec       *Recorder
	url       string
	apiKey    string
	interval  time.Duration
	resource  map[string]string
	client    *http.Client
	logger    *slog.Logger
	startNano uint64
}

// NewReporter builds a Reporter. endpoint is the SpanBarn base URL (e.g.
// http://spanbarn-ingest:8080); resource holds resource-level attributes such as
// service.name and spanbarn.mode. startTimeNano is the process start time used as
// the start of cumulative sums.
func NewReporter(rec *Recorder, endpoint, apiKey string, interval time.Duration, resource map[string]string, startTimeNano uint64, logger *slog.Logger) *Reporter {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Reporter{
		rec:       rec,
		url:       strings.TrimRight(endpoint, "/") + "/v1/metrics",
		apiKey:    apiKey,
		interval:  interval,
		resource:  resource,
		client:    &http.Client{Timeout: 10 * time.Second},
		logger:    logger,
		startNano: startTimeNano,
	}
}

// Run exports on each tick until ctx is cancelled.
func (rp *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(rp.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rp.flush(ctx)
		}
	}
}

func (rp *Reporter) flush(ctx context.Context) {
	req := rp.buildRequest(rp.rec.snapshot(), uint64(time.Now().UnixNano()))
	body, err := protojson.Marshal(req)
	if err != nil {
		rp.logger.Warn("self-metrics marshal failed", "error", err)
		return
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rp.url, bytes.NewReader(body))
	if err != nil {
		rp.logger.Warn("self-metrics request build failed", "error", err)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-SpanBarn-Api-Key", rp.apiKey)
	httpReq.Header.Set("User-Agent", selfInstrumentUA)
	resp, err := rp.client.Do(httpReq)
	if err != nil {
		rp.logger.Warn("self-metrics export failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rp.logger.Warn("self-metrics export rejected", "status", resp.StatusCode)
	}
}

// buildRequest converts a snapshot into an OTLP ExportMetricsServiceRequest.
func (rp *Reporter) buildRequest(snap snapshot, nowNano uint64) *collectormetricspb.ExportMetricsServiceRequest {
	var metrics []*metricspb.Metric

	// Request counter (cumulative monotonic sum), one point per status class.
	if len(snap.requests) > 0 {
		classes := make([]string, 0, len(snap.requests))
		for c := range snap.requests {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		dps := make([]*metricspb.NumberDataPoint, 0, len(classes))
		for _, c := range classes {
			dps = append(dps, &metricspb.NumberDataPoint{
				StartTimeUnixNano: rp.startNano,
				TimeUnixNano:      nowNano,
				Attributes:        kv(map[string]string{"status": c}),
				Value:             &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(snap.requests[c])},
			})
		}
		metrics = append(metrics, &metricspb.Metric{
			Name: metricRequests,
			Unit: "1",
			Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
				DataPoints:             dps,
			}},
		})
	}

	// Request latency (delta histogram), so each point reflects that interval.
	if snap.durCount > 0 {
		bucketCounts := make([]uint64, len(snap.durCounts))
		for i, c := range snap.durCounts {
			bucketCounts[i] = uint64(c)
		}
		sum := snap.durSum
		metrics = append(metrics, &metricspb.Metric{
			Name: metricDuration,
			Unit: "ms",
			Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints: []*metricspb.HistogramDataPoint{{
					StartTimeUnixNano: rp.startNano,
					TimeUnixNano:      nowNano,
					Count:             uint64(snap.durCount),
					Sum:               &sum,
					BucketCounts:      bucketCounts,
					ExplicitBounds:    snap.durBounds,
				}},
			}},
		})
	}

	// Rollups persisted (cumulative monotonic sum).
	metrics = append(metrics, &metricspb.Metric{
		Name: metricRollups,
		Unit: "1",
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
			IsMonotonic:            true,
			DataPoints: []*metricspb.NumberDataPoint{{
				StartTimeUnixNano: rp.startNano,
				TimeUnixNano:      nowNano,
				Value:             &metricspb.NumberDataPoint_AsDouble{AsDouble: float64(snap.rollups)},
			}},
		}},
	})

	// Sampled gauges (spool size, queue depth).
	for _, g := range snap.gauges {
		metrics = append(metrics, &metricspb.Metric{
			Name: g.name,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{
					TimeUnixNano: nowNano,
					Attributes:   kv(g.attrs),
					Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: g.value},
				}},
			}},
		})
	}

	return &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource:     &resourcepb.Resource{Attributes: kv(rp.resource)},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: metrics}},
		}},
	}
}

// kv builds OTLP string attributes from a map, in sorted key order.
func kv(m map[string]string) []*commonpb.KeyValue {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*commonpb.KeyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, &commonpb.KeyValue{
			Key:   k,
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: m[k]}},
		})
	}
	return out
}
