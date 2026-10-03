package api

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCRecoveryInterceptorConvertsPanic(t *testing.T) {
	_, err := grpcRecoveryInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/t/M"},
		func(context.Context, any) (any, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}
