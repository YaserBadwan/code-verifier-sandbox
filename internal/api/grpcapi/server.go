// Package grpcapi exposes the judge over gRPC.
package grpcapi

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/judge"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

type service struct {
	pb.UnimplementedVerifierServiceServer
	judge *judge.Service
}

func (s *service) Verify(ctx context.Context, req *pb.VerifyRequest) (*pb.VerifyResponse, error) {
	return s.judge.Verify(ctx, req, nil)
}

func (s *service) VerifyStream(req *pb.VerifyRequest, stream grpc.ServerStreamingServer[pb.VerifyEvent]) error {
	_, err := s.judge.Verify(stream.Context(), req, stream.Send)
	return err
}

func (s *service) ListLanguages(ctx context.Context, _ *pb.ListLanguagesRequest) (*pb.ListLanguagesResponse, error) {
	return &pb.ListLanguagesResponse{Languages: s.judge.Languages(ctx)}, nil
}

// New builds a gRPC server with the verifier, the standard health service and reflection
// so tools like grpcurl work without the proto file.
func New(j *judge.Service, log *slog.Logger, maxRecvBytes, maxSendBytes int) (*grpc.Server, *health.Server) {
	srv := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRecvBytes),
		grpc.MaxSendMsgSize(maxSendBytes),
		// Stop waits for running handlers too, so their containers are gone before we clean up
		grpc.WaitForHandlers(true),
		grpc.ChainUnaryInterceptor(unaryInterceptor(log)),
		grpc.ChainStreamInterceptor(streamInterceptor(log)),
	)
	pb.RegisterVerifierServiceServer(srv, &service{judge: j})

	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	healthSrv.SetServingStatus(pb.VerifierService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_NOT_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)
	reflection.Register(srv)
	return srv, healthSrv
}

// SetReady flips both the overall and the verifier health status.
func SetReady(h *health.Server, ready bool) {
	st := healthpb.HealthCheckResponse_NOT_SERVING
	if ready {
		st = healthpb.HealthCheckResponse_SERVING
	}
	h.SetServingStatus("", st)
	h.SetServingStatus(pb.VerifierService_ServiceDesc.ServiceName, st)
}

func unaryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		started := time.Now()
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in grpc handler", "method", info.FullMethod, "panic", p, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal server error")
			}
			logCall(log, info.FullMethod, started, err)
		}()
		return handler(ctx, req)
	}
}

func streamInterceptor(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		started := time.Now()
		defer func() {
			if p := recover(); p != nil {
				log.Error("panic in grpc stream", "method", info.FullMethod, "panic", p, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal server error")
			}
			logCall(log, info.FullMethod, started, err)
		}()
		return handler(srv, ss)
	}
}

func logCall(log *slog.Logger, method string, started time.Time, err error) {
	code := status.Code(err)
	attrs := []any{"method", method, "code", code.String(), "took_ms", time.Since(started).Milliseconds()}
	switch code {
	case codes.OK, codes.Canceled:
		log.Debug("grpc call", attrs...)
	case codes.Internal, codes.Unknown:
		log.Error("grpc call failed", append(attrs, "error", status.Convert(err).Message())...)
	default:
		log.Info("grpc call rejected", append(attrs, "error", status.Convert(err).Message())...)
	}
}
