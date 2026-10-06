// Command verifierctl is a small client for the verifier gRPC API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

const usage = `verifierctl talks to a running verifier over gRPC.

Usage
  verifierctl run -file solution.py -tests tests.json [options]
  verifierctl languages [-addr host:port]
  verifierctl health [-addr host:port]

Run "verifierctl <command> -h" for the options of a command.
The server address defaults to 127.0.0.1:50051 or VERIFIER_ADDR when set.
`

// exit codes, so scripts can tell a failed submission apart from a broken setup
const (
	exitAccepted = 0
	exitRejected = 1
	exitError    = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return exitError
		}
		return exitAccepted
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	code := exitAccepted
	switch args[0] {
	case "run":
		code, err = runCommand(ctx, args[1:], stdout, stderr)
	case "languages":
		err = languagesCommand(ctx, args[1:], stdout, stderr)
	case "health":
		code, err = healthCommand(ctx, args[1:], stdout, stderr)
	default:
		err = fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
	if errors.Is(err, flag.ErrHelp) {
		return exitAccepted
	}
	if err != nil && ctx.Err() != nil {
		fmt.Fprintln(stderr, "cancelled")
		return exitError
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", describe(err))
		return exitError
	}
	return code
}

func describe(err error) string {
	if st, ok := status.FromError(err); ok {
		return fmt.Sprintf("%s (%s)", st.Message(), st.Code())
	}
	return err.Error()
}

func defaultAddr() string {
	if addr := os.Getenv("VERIFIER_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:50051"
}

func dial(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256<<20), grpc.MaxCallSendMsgSize(256<<20)),
	)
}

func newFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("verifierctl "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", defaultAddr(), "verifier gRPC address")
	return fs, addr
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, addr := newFlags("run", stderr)
	file := fs.String("file", "", "source file to verify (required)")
	testsPath := fs.String("tests", "", "JSON file with test cases, or a folder of NAME.in and NAME.out files (required)")
	lang := fs.String("lang", "", "language id, guessed from the file extension when empty")
	timeLimit := fs.Uint("time-limit", 0, "per test time limit in milliseconds, 0 uses the server default")
	memory := fs.Uint("memory", 0, "per test memory limit in MB, 0 uses the server default")
	mode := fs.String("compare", "trailing", "output comparison, one of exact, trailing or tokens")
	failFast := fs.Bool("fail-fast", false, "skip the remaining tests after the first failure")
	asJSON := fs.Bool("json", false, "print the full result as JSON instead of a table")
	showOutput := fs.Bool("show-output", false, "print stdout and stderr of failed tests")
	if err := fs.Parse(args); err != nil {
		return exitError, err
	}
	if *file == "" || *testsPath == "" {
		fs.Usage()
		return exitError, errors.New("-file and -tests are required")
	}

	source, err := os.ReadFile(*file)
	if err != nil {
		return exitError, err
	}
	if *lang == "" {
		l, ok := languages.ByExtension(filepath.Ext(*file))
		if !ok {
			return exitError, fmt.Errorf("can't tell the language of %s from its extension, pass -lang", *file)
		}
		*lang = l.ID
	}
	tests, err := loadTests(*testsPath)
	if err != nil {
		return exitError, err
	}
	compareMode, err := parseCompareMode(*mode)
	if err != nil {
		return exitError, err
	}

	req := &pb.VerifyRequest{
		Language:           *lang,
		SourceCode:         string(source),
		TestCases:          tests,
		TimeLimitMs:        uint32(*timeLimit),
		MemoryLimitMb:      uint32(*memory),
		CompareMode:        compareMode,
		StopOnFirstFailure: *failFast,
	}

	conn, err := dial(*addr)
	if err != nil {
		return exitError, err
	}
	defer conn.Close()
	client := pb.NewVerifierServiceClient(conn)

	if *asJSON {
		resp, err := client.Verify(ctx, req)
		if err != nil {
			return exitError, err
		}
		out, _ := protojson.MarshalOptions{Multiline: true, UseProtoNames: true, EmitUnpopulated: true}.Marshal(resp)
		fmt.Fprintln(stdout, string(out))
		return verdictExit(resp.Verdict), nil
	}

	stream, err := client.VerifyStream(ctx, req)
	if err != nil {
		return exitError, err
	}
	printer := newPrinter(stdout, *showOutput, tests)
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return exitError, errors.New("the server closed the stream without a summary")
		}
		if err != nil {
			return exitError, err
		}
		switch e := ev.Event.(type) {
		case *pb.VerifyEvent_Compile:
			printer.compile(e.Compile)
		case *pb.VerifyEvent_TestResult:
			printer.test(e.TestResult)
		case *pb.VerifyEvent_Summary:
			printer.summary(e.Summary)
			return verdictExit(e.Summary.Verdict), nil
		}
	}
}

func verdictExit(v pb.Verdict) int {
	if v == pb.Verdict_VERDICT_ACCEPTED {
		return exitAccepted
	}
	return exitRejected
}

func parseCompareMode(s string) (pb.CompareMode, error) {
	switch strings.ToLower(s) {
	case "exact":
		return pb.CompareMode_COMPARE_MODE_EXACT, nil
	case "trailing", "":
		return pb.CompareMode_COMPARE_MODE_IGNORE_TRAILING_WHITESPACE, nil
	case "tokens":
		return pb.CompareMode_COMPARE_MODE_TOKENS, nil
	}
	return 0, fmt.Errorf("unknown compare mode %q, use exact, trailing or tokens", s)
}

func languagesCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, addr := newFlags("languages", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	conn, err := dial(*addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := pb.NewVerifierServiceClient(conn).ListLanguages(ctx, &pb.ListLanguagesRequest{})
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tREADY\tALIASES\tIMAGE")
	for _, l := range resp.Languages {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%s\t%s\n", l.Id, l.Name, l.Ready, strings.Join(l.Aliases, ","), l.Image)
	}
	return tw.Flush()
}

func healthCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, addr := newFlags("health", stderr)
	if err := fs.Parse(args); err != nil {
		return exitError, err
	}
	conn, err := dial(*addr)
	if err != nil {
		return exitError, err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return exitError, err
	}
	fmt.Fprintln(stdout, resp.Status)
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		return exitRejected, nil
	}
	return exitAccepted, nil
}
