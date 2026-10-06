package grpcapi

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/judge"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox/sandboxtest"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

func startServer(t *testing.T, maxMessage int) (pb.VerifierServiceClient, *grpc.ClientConn, func(bool)) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	langs, _ := languages.Select([]string{"python"})
	j := judge.New(judge.Config{
		Runner: sandboxtest.NewRunner(), Images: sandboxtest.Images{}, Languages: langs,
		Limits: judge.Limits{
			DefaultTimeLimit: time.Second, MaxTimeLimit: 5 * time.Second,
			DefaultMemoryMB: 128, MaxMemoryMB: 512, CompileTimeout: 10 * time.Second, CompileMemoryMB: 512,
			OutputLimitBytes: 1 << 20, ReportBytes: 1 << 16, MaxSourceBytes: 1 << 16, MaxTests: 10,
		},
		MaxConcurrent: 2, MaxQueue: 4, Log: log,
	})
	srv, healthSrv := New(j, log, maxMessage, 1<<24)
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewVerifierServiceClient(conn), conn, func(ready bool) { SetReady(healthSrv, ready) }
}

func request() *pb.VerifyRequest {
	return &pb.VerifyRequest{
		Language: "python", SourceCode: "print(input())",
		TestCases: []*pb.TestCase{{Name: "first", Input: "7", ExpectedOutput: "7"}, {Input: "8", ExpectedOutput: "9"}},
	}
}

func TestVerify(t *testing.T) {
	client, _, _ := startServer(t, 1<<20)
	resp, err := client.Verify(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_WRONG_ANSWER || resp.Passed != 1 || resp.Results[0].Name != "first" {
		t.Fatalf("unexpected response %v", resp)
	}
}

func TestVerifyStream(t *testing.T) {
	client, _, _ := startServer(t, 1<<20)
	stream, err := client.VerifyStream(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case ev.GetCompile() != nil:
			kinds = append(kinds, "compile")
		case ev.GetTestResult() != nil:
			kinds = append(kinds, "test")
		case ev.GetSummary() != nil:
			kinds = append(kinds, "summary")
		}
	}
	if strings.Join(kinds, ",") != "compile,test,test,summary" {
		t.Fatalf("unexpected events %v", kinds)
	}
}

func TestValidationErrorCode(t *testing.T) {
	client, _, _ := startServer(t, 1<<20)
	req := request()
	req.Language = "cobol"
	_, err := client.Verify(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	stream, _ := client.VerifyStream(context.Background(), req)
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument on stream, got %v", err)
	}
}

func TestMessageTooLarge(t *testing.T) {
	client, _, _ := startServer(t, 1024)
	req := request()
	req.TestCases[0].Input = strings.Repeat("x", 4096)
	_, err := client.Verify(context.Background(), req)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted for an oversized request, got %v", err)
	}
}

func TestListLanguages(t *testing.T) {
	client, _, _ := startServer(t, 1<<20)
	resp, err := client.ListLanguages(context.Background(), &pb.ListLanguagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Languages) != 1 || resp.Languages[0].Id != "python" || !resp.Languages[0].Ready {
		t.Fatalf("unexpected languages %v", resp.Languages)
	}
}

func TestHealth(t *testing.T) {
	_, conn, setReady := startServer(t, 1<<20)
	h := healthpb.NewHealthClient(conn)
	check := func() healthpb.HealthCheckResponse_ServingStatus {
		resp, err := h.Check(context.Background(), &healthpb.HealthCheckRequest{Service: "verifier.v1.VerifierService"})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Status
	}
	if check() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatal("should start as not serving")
	}
	setReady(true)
	if check() != healthpb.HealthCheckResponse_SERVING {
		t.Fatal("should be serving after SetReady")
	}
}
