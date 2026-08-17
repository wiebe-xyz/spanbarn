package api

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// GRPCServer wraps a *grpc.Server with OTLP trace and metrics services registered.
type GRPCServer struct {
	server *grpc.Server
	logger *slog.Logger
}

// NewGRPCServer creates a gRPC server with auth interceptor and registers
// the OTLP TraceService and MetricsService backed by the given HTTP server's
// ingest handlers.
func NewGRPCServer(s *Server, logger *slog.Logger) *GRPCServer {
	if logger == nil {
		logger = slog.Default()
	}
	// The OTLP gRPC surface never touches the HTTP mux, so the shed middleware
	// on the HTTP routes does not cover it — gating only HTTP would read as
	// protection while leaving this path wide open. Admission runs after auth,
	// matching the HTTP ordering: capacity state is internal, so an
	// unauthenticated caller gets Unauthenticated rather than Unavailable.
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcAuthInterceptor(s),
			s.admission.UnaryInterceptor(),
		),
	)
	collectortracepb.RegisterTraceServiceServer(srv, &grpcTraceServer{s: s})
	collectormetricspb.RegisterMetricsServiceServer(srv, &grpcMetricsServer{s: s})
	collectorlogspb.RegisterLogsServiceServer(srv, &grpcLogsServer{s: s})
	return &GRPCServer{server: srv, logger: logger}
}

// ListenAndServe binds addr and serves until ctx is cancelled, then calls
// GracefulStop to wait for in-flight RPCs to complete.
func (g *GRPCServer) ListenAndServe(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		g.logger.Info("grpc listening", "addr", addr)
		errCh <- g.server.Serve(lis)
	}()
	select {
	case <-ctx.Done():
		g.server.GracefulStop()
		return nil
	case err := <-errCh:
		return err
	}
}

// Serve exposes the underlying *grpc.Server.Serve for in-process testing with bufconn.
func (g *GRPCServer) Serve(lis net.Listener) error {
	return g.server.Serve(lis)
}

// Stop stops the gRPC server immediately, used in tests.
func (g *GRPCServer) Stop() {
	g.server.Stop()
}

// grpcTraceServer implements the OTLP TraceService, delegating to s.ingest.
type grpcTraceServer struct {
	collectortracepb.UnimplementedTraceServiceServer
	s *Server
}

func (t *grpcTraceServer) Export(ctx context.Context, req *collectortracepb.ExportTraceServiceRequest) (*collectortracepb.ExportTraceServiceResponse, error) {
	projectID := GetProjectID(ctx)
	records := otlpToSpanRecords(req, projectID)
	if t.s.traceBuffer != nil {
		for _, rec := range records {
			t.s.traceBuffer.Add(rec)
		}
	} else if t.s.ingest != nil {
		for _, rec := range records {
			t.s.ingest.Enqueue(rec)
		}
	}
	return &collectortracepb.ExportTraceServiceResponse{}, nil
}

// grpcMetricsServer implements the OTLP MetricsService, delegating to s.metricsIngest.
type grpcMetricsServer struct {
	collectormetricspb.UnimplementedMetricsServiceServer
	s *Server
}

func (m *grpcMetricsServer) Export(ctx context.Context, req *collectormetricspb.ExportMetricsServiceRequest) (*collectormetricspb.ExportMetricsServiceResponse, error) {
	projectID := GetProjectID(ctx)
	recs := otlpToMetricRecords(req, projectID)
	if m.s.metricsIngest != nil {
		m.s.metricsIngest.Enqueue(recs)
	}
	return &collectormetricspb.ExportMetricsServiceResponse{}, nil
}

// grpcLogsServer implements the OTLP LogsService, delegating to s.logsIngest.
type grpcLogsServer struct {
	collectorlogspb.UnimplementedLogsServiceServer
	s *Server
}

func (l *grpcLogsServer) Export(ctx context.Context, req *collectorlogspb.ExportLogsServiceRequest) (*collectorlogspb.ExportLogsServiceResponse, error) {
	projectID := GetProjectID(ctx)
	recs := otlpToLogRecords(req, projectID)
	if l.s.logsIngest != nil {
		l.s.logsIngest.Enqueue(recs)
	}
	return &collectorlogspb.ExportLogsServiceResponse{}, nil
}

// grpcAuthInterceptor returns a gRPC unary server interceptor that mirrors the
// two HTTP auth modes: per-project DB keys when s.authorizer is set, static key otherwise.
func grpcAuthInterceptor(s *Server) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		key := extractGRPCKey(md)
		if key == "" {
			return nil, status.Error(codes.Unauthenticated, "missing api key")
		}

		if s.authorizer != nil {
			projectID, scope, err := s.authorizer.Authorize(key)
			if err != nil {
				return nil, status.Error(codes.Unauthenticated, "invalid api key")
			}
			ctx = SetProjectID(ctx, projectID)
			ctx = SetScope(ctx, scope)
		} else {
			if key != s.apiKey {
				return nil, status.Error(codes.Unauthenticated, "invalid api key")
			}
			ctx = SetProjectID(ctx, 0)
			ctx = SetScope(ctx, "ingest")
		}

		return handler(ctx, req)
	}
}

// extractGRPCKey reads the API key from x-spanbarn-api-key metadata or
// Authorization: Bearer <key> metadata, whichever is present first.
func extractGRPCKey(md metadata.MD) string {
	if vals := md.Get("x-spanbarn-api-key"); len(vals) > 0 && vals[0] != "" {
		return vals[0]
	}
	if vals := md.Get("authorization"); len(vals) > 0 {
		return strings.TrimPrefix(vals[0], "Bearer ")
	}
	return ""
}
