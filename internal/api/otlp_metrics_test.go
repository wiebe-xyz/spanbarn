package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/model"

	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// captureMetricsRepo records InsertMetrics calls.
type captureMetricsRepo struct {
	mu      sync.Mutex
	records []model.MetricRecord
}

func (r *captureMetricsRepo) InsertMetrics(_ context.Context, recs []model.MetricRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, recs...)
	return nil
}

func (r *captureMetricsRepo) all() []model.MetricRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.MetricRecord{}, r.records...)
}

// setupMetricsTestServer creates a Server with a MetricsHandler but does NOT start Run,
// so records stay buffered for inspection via the repo capture.
func setupMetricsTestServer(t *testing.T) (*httptest.Server, *captureMetricsRepo) {
	t.Helper()
	repo := &captureMetricsRepo{}
	mh := ingest.NewMetricsHandler(repo, slog.Default())

	srv := api.NewServerWithQuery(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, nil, nil, nil, slog.Default(), api.WithMetricsHandler(mh))

	// Start the handler so Enqueue → InsertMetrics pipeline works in tests.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go mh.Run(ctx)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, repo
}

// buildGaugeRequest builds a minimal ExportMetricsServiceRequest with one gauge data point.
func buildGaugeRequest(metricName string, value float64) *collectormetricspb.ExportMetricsServiceRequest {
	return &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "test-svc"}}},
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: metricName,
								Unit: "1",
								Data: &metricspb.Metric_Gauge{
									Gauge: &metricspb.Gauge{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												TimeUnixNano: 1700000000000000000,
												Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: value},
												Attributes: []*commonpb.KeyValue{
													{Key: "host", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "server-1"}}},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func postMetrics(t *testing.T, ts *httptest.Server, req *collectormetricspb.ExportMetricsServiceRequest, contentType string) *http.Response {
	t.Helper()
	var body []byte
	if contentType == "application/json" {
		var err error
		body, err = protojson.Marshal(req)
		if err != nil {
			t.Fatalf("protojson.Marshal: %v", err)
		}
	} else {
		var err error
		body, err = proto.Marshal(req)
		if err != nil {
			t.Fatalf("proto.Marshal: %v", err)
		}
		contentType = "application/x-protobuf"
	}

	httpReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/metrics", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST /v1/metrics: %v", err)
	}
	return resp
}

func waitForRecords(t *testing.T, repo *captureMetricsRepo, want int) []model.MetricRecord {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if recs := repo.all(); len(recs) >= want {
			return recs
		}
		time.Sleep(10 * time.Millisecond)
	}
	return repo.all()
}

func TestOTLPMetricsProtobuf(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	resp := postMetrics(t, ts, buildGaugeRequest("cpu.usage", 0.85), "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want at least 1 record, got 0")
	}
	if recs[0].Name != "cpu.usage" {
		t.Errorf("name: want cpu.usage, got %s", recs[0].Name)
	}
	if recs[0].Type != model.MetricTypeGauge {
		t.Errorf("type: want gauge, got %s", recs[0].Type)
	}
}

func TestOTLPMetricsJSON(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	resp := postMetrics(t, ts, buildGaugeRequest("mem.usage", 0.42), "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("want at least 1 record, got 0")
	}
}

func TestOTLPMetricsSum(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	req := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "requests.total",
					Data: &metricspb.Metric_Sum{
						Sum: &metricspb.Sum{
							DataPoints: []*metricspb.NumberDataPoint{{
								TimeUnixNano: 1700000000000000000,
								Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1000},
							}},
						},
					},
				}},
			}},
		}},
	}
	resp := postMetrics(t, ts, req, "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	if recs[0].Type != model.MetricTypeSum {
		t.Errorf("type: want sum, got %s", recs[0].Type)
	}
	if recs[0].Value != 1000 {
		t.Errorf("value: want 1000, got %v", recs[0].Value)
	}
}

func TestOTLPMetricsHistogram(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	req := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "http.duration",
					Unit: "ms",
					Data: &metricspb.Metric_Histogram{
						Histogram: &metricspb.Histogram{
							DataPoints: []*metricspb.HistogramDataPoint{{
								TimeUnixNano:   1700000000000000000,
								Count:          42,
								Sum:            float64ptr(3456.7),
								ExplicitBounds: []float64{5, 10, 25, 50, 100},
								BucketCounts:   []uint64{1, 5, 10, 15, 8, 3},
							}},
						},
					},
				}},
			}},
		}},
	}
	resp := postMetrics(t, ts, req, "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	if recs[0].Type != model.MetricTypeHistogram {
		t.Errorf("type: want histogram, got %s", recs[0].Type)
	}
	if recs[0].Count != 42 {
		t.Errorf("count: want 42, got %d", recs[0].Count)
	}
	if recs[0].Extra == nil {
		t.Error("extra should be non-nil for histogram")
	}
}

func TestOTLPMetricsExponentialHistogram(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	req := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "latency.exp",
					Data: &metricspb.Metric_ExponentialHistogram{
						ExponentialHistogram: &metricspb.ExponentialHistogram{
							DataPoints: []*metricspb.ExponentialHistogramDataPoint{{
								TimeUnixNano: 1700000000000000000,
								Count:        10,
								Sum:          float64ptr(500),
								Scale:        3,
								ZeroCount:    0,
								Positive:     &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: 0, BucketCounts: []uint64{1, 2, 3}},
							}},
						},
					},
				}},
			}},
		}},
	}
	resp := postMetrics(t, ts, req, "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	if recs[0].Type != model.MetricTypeExponentialHistogram {
		t.Errorf("type: want exp_histogram, got %s", recs[0].Type)
	}
}

func TestOTLPMetricsSummary(t *testing.T) {
	ts, repo := setupMetricsTestServer(t)
	req := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "response.size",
					Data: &metricspb.Metric_Summary{
						Summary: &metricspb.Summary{
							DataPoints: []*metricspb.SummaryDataPoint{{
								TimeUnixNano: 1700000000000000000,
								Count:        100,
								Sum:          8765.4,
								QuantileValues: []*metricspb.SummaryDataPoint_ValueAtQuantile{
									{Quantile: 0.5, Value: 50.0},
									{Quantile: 0.99, Value: 200.0},
								},
							}},
						},
					},
				}},
			}},
		}},
	}
	resp := postMetrics(t, ts, req, "application/x-protobuf")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	recs := waitForRecords(t, repo, 1)
	if len(recs) == 0 {
		t.Fatal("no records")
	}
	if recs[0].Type != model.MetricTypeSummary {
		t.Errorf("type: want summary, got %s", recs[0].Type)
	}
	if recs[0].Extra == nil {
		t.Error("extra should contain quantiles")
	}
}

func TestOTLPMetricsNoAPIKey(t *testing.T) {
	ts, _ := setupMetricsTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/metrics", bytes.NewReader([]byte{}))
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
}

func float64ptr(v float64) *float64 { return &v }

func TestOTLPMetricsMethodNotAllowed(t *testing.T) {
	ts, _ := setupMetricsTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/metrics", nil)
	req.Header.Set("X-SpanBarn-Api-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", resp.StatusCode)
	}
}
