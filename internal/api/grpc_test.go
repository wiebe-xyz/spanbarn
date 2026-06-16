package api_test

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/wiebe-xyz/spanbarn/internal/api"
	"github.com/wiebe-xyz/spanbarn/internal/ingest"
	"github.com/wiebe-xyz/spanbarn/internal/model"
	"github.com/wiebe-xyz/spanbarn/internal/spool"

	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1 << 20

// setupGRPCTest creates a GRPCServer over an in-memory listener and returns
// gRPC clients for both trace and metrics services.
func setupGRPCTest(t *testing.T) (
	traceSvc collectortracepb.TraceServiceClient,
	metricsSvc collectormetricspb.MetricsServiceClient,
	repo *captureMetricsRepo,
	queue *ingest.Queue,
) {
	t.Helper()

	// Span ingest: real queue + spool (handler not started, so records stay in queue)
	q := ingest.NewQueue(1024)
	sp, err := spool.NewSpool(t.TempDir(), spool.DefaultMaxBytes)
	if err != nil {
		t.Fatalf("spool: %v", err)
	}
	t.Cleanup(func() { sp.Close() })
	ingestHandler := ingest.NewHandler(q, sp, 0, slog.Default())

	// Metrics ingest: mock repo, handler started
	repo = &captureMetricsRepo{}
	mh := ingest.NewMetricsHandler(repo, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go mh.Run(ctx)

	srv := api.NewServerWithQuery(api.ServerConfig{
		APIKey:       testAPIKey,
		MaxBodyBytes: 4 << 20,
		Version:      "test",
	}, ingestHandler, nil, nil, slog.Default(), api.WithMetricsHandler(mh))

	grpcSrv := api.NewGRPCServer(srv, slog.Default())

	lis := bufconn.Listen(bufSize)
	t.Cleanup(grpcSrv.Stop)
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	return collectortracepb.NewTraceServiceClient(conn),
		collectormetricspb.NewMetricsServiceClient(conn),
		repo, q
}

func authCtx(key string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-spanbarn-api-key", key))
}

func TestGRPCTraceExport(t *testing.T) {
	traceSvc, _, _, q := setupGRPCTest(t)

	req := &collectortracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "grpc-svc"}}},
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           make([]byte, 16),
					SpanId:            make([]byte, 8),
					Name:              "grpc-test-span",
					StartTimeUnixNano: 1700000000000000000,
					EndTimeUnixNano:   1700000001000000000,
				}},
			}},
		}},
	}

	_, err := traceSvc.Export(authCtx(testAPIKey), req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Drain the queue to verify the span arrived.
	records := q.Drain()
	if len(records) != 1 {
		t.Errorf("want 1 span in queue, got %d", len(records))
	}
	if len(records) > 0 && records[0].Name != "grpc-test-span" {
		t.Errorf("span name: want grpc-test-span, got %s", records[0].Name)
	}
}

func TestGRPCMetricsExport(t *testing.T) {
	_, metricsSvc, repo, _ := setupGRPCTest(t)

	req := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "grpc.test.gauge",
					Data: &metricspb.Metric_Gauge{
						Gauge: &metricspb.Gauge{
							DataPoints: []*metricspb.NumberDataPoint{{
								TimeUnixNano: 1700000000000000000,
								Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 3.14},
							}},
						},
					},
				}},
			}},
		}},
	}

	_, err := metricsSvc.Export(authCtx(testAPIKey), req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Wait for the metrics handler to flush.
	deadline := time.Now().Add(500 * time.Millisecond)
	var recs []model.MetricRecord
	for time.Now().Before(deadline) {
		recs = repo.all()
		if len(recs) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(recs) == 0 {
		t.Fatal("want 1 metric record, got 0")
	}
	if recs[0].Name != "grpc.test.gauge" {
		t.Errorf("name: want grpc.test.gauge, got %s", recs[0].Name)
	}
}

func TestGRPCAuthMissing(t *testing.T) {
	traceSvc, _, _, _ := setupGRPCTest(t)

	_, err := traceSvc.Export(context.Background(), &collectortracepb.ExportTraceServiceRequest{})
	if err == nil {
		t.Fatal("want Unauthenticated error, got nil")
	}
}

func TestGRPCAuthInvalid(t *testing.T) {
	traceSvc, _, _, _ := setupGRPCTest(t)

	_, err := traceSvc.Export(authCtx("wrong-key"), &collectortracepb.ExportTraceServiceRequest{})
	if err == nil {
		t.Fatal("want Unauthenticated error, got nil")
	}
}

func TestGRPCAuthBearerFallback(t *testing.T) {
	traceSvc, _, _, q := setupGRPCTest(t)

	// Use Authorization: Bearer header instead of x-spanbarn-api-key.
	bearerCtx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+testAPIKey))

	_, err := traceSvc.Export(bearerCtx, &collectortracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: make([]byte, 16),
					SpanId:  make([]byte, 8),
					Name:    "bearer-span",
				}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("bearer auth: %v", err)
	}

	records := q.Drain()
	if len(records) != 1 {
		t.Errorf("bearer auth: want 1 span, got %d", len(records))
	}
}
