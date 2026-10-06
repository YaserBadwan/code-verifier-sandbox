package judge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox/sandboxtest"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

func testLimits() Limits {
	return Limits{
		DefaultTimeLimit: 3 * time.Second, MaxTimeLimit: 10 * time.Second,
		DefaultMemoryMB: 256, MaxMemoryMB: 1024,
		CompileTimeout: 30 * time.Second, CompileMemoryMB: 1024,
		OutputLimitBytes: 1 << 20, ReportBytes: 1024,
		MaxSourceBytes: 1000, MaxTests: 5,
	}
}

func newService(t *testing.T, r *sandboxtest.Runner, mutate func(*Config)) *Service {
	t.Helper()
	langs, _ := languages.Select([]string{"python", "cpp", "java"})
	cfg := Config{
		Runner: r, Images: sandboxtest.Images{}, Languages: langs, Limits: testLimits(),
		MaxConcurrent: 2, MaxQueue: 10,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg)
}

func request(tests ...*pb.TestCase) *pb.VerifyRequest {
	return &pb.VerifyRequest{Language: "python", SourceCode: "print(input())", TestCases: tests}
}

func tc(in, out string) *pb.TestCase { return &pb.TestCase{Input: in, ExpectedOutput: out} }

func code(err error) codes.Code { return status.Code(err) }

func TestAcceptedAndWrongAnswer(t *testing.T) {
	r := sandboxtest.NewRunner()
	s := newService(t, r, nil)
	resp, err := s.Verify(context.Background(), request(tc("1\n", "1"), tc("2\n", "3")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_WRONG_ANSWER || resp.Passed != 1 || resp.Total != 2 {
		t.Fatalf("unexpected summary %v", resp)
	}
	if resp.Results[0].Verdict != pb.Verdict_VERDICT_ACCEPTED || resp.Results[1].Verdict != pb.Verdict_VERDICT_WRONG_ANSWER {
		t.Fatalf("unexpected verdicts %v", resp.Results)
	}
	if !strings.Contains(resp.Results[1].Message, "line 1") {
		t.Fatalf("wrong answer should say where it differs, got %q", resp.Results[1].Message)
	}
	if resp.SubmissionId == "" || !resp.Compile.Success {
		t.Fatalf("missing id or compile result %v", resp)
	}
	if r.LiveVolumes() != 0 {
		t.Fatal("volume was not removed")
	}
}

func TestAllAccepted(t *testing.T) {
	s := newService(t, sandboxtest.NewRunner(), nil)
	resp, err := s.Verify(context.Background(), request(tc("a", "a"), tc("b\n", "b\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_ACCEPTED || resp.Passed != 2 {
		t.Fatalf("expected accepted, got %v", resp)
	}
}

func TestDefaultLimitsAndOverrides(t *testing.T) {
	r := sandboxtest.NewRunner()
	s := newService(t, r, nil)
	if _, err := s.Verify(context.Background(), request(tc("x", "x")), nil); err != nil {
		t.Fatal(err)
	}
	req := request(tc("x", "x"))
	req.TimeLimitMs = 500
	req.MemoryLimitMb = 64
	if _, err := s.Verify(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	specs := r.RunSpecs()
	if specs[0].Timeout != 3*time.Second || specs[0].MemoryBytes != 256<<20 {
		t.Fatalf("defaults not applied, got %v %d", specs[0].Timeout, specs[0].MemoryBytes)
	}
	if specs[1].Timeout != 500*time.Millisecond || specs[1].MemoryBytes != 64<<20 {
		t.Fatalf("overrides not applied, got %v %d", specs[1].Timeout, specs[1].MemoryBytes)
	}
	if specs[0].OutputLimit != 1<<20 || specs[0].WritableVolume || len(specs[0].Files) != 0 {
		t.Fatalf("run spec is not locked down %+v", specs[0])
	}
}

func TestCompileSpecGetsSource(t *testing.T) {
	r := sandboxtest.NewRunner()
	s := newService(t, r, nil)
	req := request(tc("x", "x"))
	req.Language = "java"
	req.SourceCode = "public class Main {}"
	if _, err := s.Verify(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	compile := r.Specs()[0]
	if !compile.WritableVolume || len(compile.Files) != 1 || compile.Files[0].Name != "Main.java" || string(compile.Files[0].Data) != req.SourceCode {
		t.Fatalf("compile spec is wrong %+v", compile)
	}
	if compile.Image != "eclipse-temurin:25-jdk-alpine" || compile.Timeout != 30*time.Second {
		t.Fatalf("compile spec is wrong %+v", compile)
	}
}

func TestCompilationError(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 1, Stderr: []byte("main.py line 1 SyntaxError")}, nil
	}
	s := newService(t, r, nil)
	resp, err := s.Verify(context.Background(), request(tc("1", "1"), tc("2", "2")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_COMPILATION_ERROR || resp.Compile.Success {
		t.Fatalf("expected compilation error, got %v", resp)
	}
	if !strings.Contains(resp.Compile.Output, "SyntaxError") {
		t.Fatalf("compiler output missing, got %q", resp.Compile.Output)
	}
	for _, res := range resp.Results {
		if res.Verdict != pb.Verdict_VERDICT_SKIPPED {
			t.Fatalf("tests should be skipped, got %v", res.Verdict)
		}
	}
	if r.Runs() != 0 {
		t.Fatal("nothing should run after a failed compile")
	}
}

func TestCompileTimeoutAndMemory(t *testing.T) {
	cases := map[string]struct {
		res  sandbox.Result
		want string
	}{
		"timeout": {sandbox.Result{TimedOut: true, ExitCode: 137}, "took longer than"},
		"memory":  {sandbox.Result{OOMKilled: true, ExitCode: 137}, "ran out of memory"},
		"output":  {sandbox.Result{OutputLimitExceeded: true, ExitCode: 137}, "too much output"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := sandboxtest.NewRunner()
			r.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) { return c.res, nil }
			resp, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1")), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict != pb.Verdict_VERDICT_COMPILATION_ERROR || !strings.Contains(resp.Compile.Output, c.want) {
				t.Fatalf("got %v %q", resp.Verdict, resp.Compile.Output)
			}
		})
	}
}

func TestJavaClassNameHint(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) {
		return sandbox.Result{ExitCode: 1, Stderr: []byte("Main.java:1: error: class Solution is public, should be declared in a file named Solution.java")}, nil
	}
	req := request(tc("1", "1"))
	req.Language = "java"
	resp, err := newService(t, r, nil).Verify(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Compile.Output, "must be named Main") {
		t.Fatalf("expected a hint about the class name, got %q", resp.Compile.Output)
	}
}

func TestVerdictMapping(t *testing.T) {
	cases := []struct {
		name    string
		res     sandbox.Result
		verdict pb.Verdict
		message string
	}{
		{"timeout", sandbox.Result{TimedOut: true, ExitCode: 137}, pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED, "time limit"},
		{"output", sandbox.Result{OutputLimitExceeded: true, ExitCode: 137}, pb.Verdict_VERDICT_OUTPUT_LIMIT_EXCEEDED, "output"},
		{"oom flag", sandbox.Result{OOMKilled: true, ExitCode: 137}, pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED, "memory limit"},
		{"sigkill without oom flag", sandbox.Result{ExitCode: 137}, pb.Verdict_VERDICT_RUNTIME_ERROR, "SIGKILL"},
		{"runtime oom marker", sandbox.Result{ExitCode: 1, Stderr: []byte("Traceback\nMemoryError")}, pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED, "memory limit"},
		{"segfault", sandbox.Result{ExitCode: 139}, pb.Verdict_VERDICT_RUNTIME_ERROR, "SIGSEGV"},
		{"abort", sandbox.Result{ExitCode: 134}, pb.Verdict_VERDICT_RUNTIME_ERROR, "SIGABRT"},
		{"exit code", sandbox.Result{ExitCode: 2, Stderr: []byte("ValueError")}, pb.Verdict_VERDICT_RUNTIME_ERROR, "exited with code 2"},
		{"timeout beats output", sandbox.Result{TimedOut: true, OutputLimitExceeded: true}, pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED, "time limit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := sandboxtest.NewRunner()
			r.RunFunc = func(context.Context, sandbox.Spec) (sandbox.Result, error) { return c.res, nil }
			resp, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1")), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := resp.Results[0]
			if got.Verdict != c.verdict || resp.Verdict != c.verdict {
				t.Fatalf("got %v overall %v, want %v", got.Verdict, resp.Verdict, c.verdict)
			}
			if !strings.Contains(got.Message, c.message) {
				t.Fatalf("message %q should contain %q", got.Message, c.message)
			}
			if int(got.ExitCode) != c.res.ExitCode {
				t.Fatalf("exit code not reported, got %d", got.ExitCode)
			}
		})
	}
}

func TestStopOnFirstFailure(t *testing.T) {
	r := sandboxtest.NewRunner()
	s := newService(t, r, nil)
	req := request(tc("1", "1"), tc("2", "nope"), tc("3", "3"))
	req.StopOnFirstFailure = true
	resp, err := s.Verify(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []pb.Verdict{pb.Verdict_VERDICT_ACCEPTED, pb.Verdict_VERDICT_WRONG_ANSWER, pb.Verdict_VERDICT_SKIPPED}
	for i, w := range want {
		if resp.Results[i].Verdict != w {
			t.Fatalf("test %d got %v, want %v", i, resp.Results[i].Verdict, w)
		}
	}
	if r.Runs() != 2 {
		t.Fatalf("expected 2 runs, got %d", r.Runs())
	}
	if resp.Verdict != pb.Verdict_VERDICT_WRONG_ANSWER {
		t.Fatalf("overall should be the first failure, got %v", resp.Verdict)
	}
}

func TestRunnerErrorIsInternalError(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.RunFunc = func(_ context.Context, spec sandbox.Spec) (sandbox.Result, error) {
		if string(spec.Stdin) == "bad" {
			return sandbox.Result{}, errors.New("daemon hiccup")
		}
		return sandbox.Result{Stdout: spec.Stdin}, nil
	}
	resp, err := newService(t, r, nil).Verify(context.Background(), request(tc("bad", "x"), tc("ok", "ok")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].Verdict != pb.Verdict_VERDICT_INTERNAL_ERROR || resp.Results[1].Verdict != pb.Verdict_VERDICT_ACCEPTED {
		t.Fatalf("got %v", resp.Results)
	}
	if resp.Verdict != pb.Verdict_VERDICT_INTERNAL_ERROR {
		t.Fatalf("overall should be internal error, got %v", resp.Verdict)
	}
	if strings.Contains(resp.Results[0].Message, "daemon hiccup") {
		t.Fatal("internal details should not leak to the client")
	}
}

func TestImageRemovedMidway(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.RunFunc = func(context.Context, sandbox.Spec) (sandbox.Result, error) {
		return sandbox.Result{}, fmt.Errorf("%w (python)", sandbox.ErrImageMissing)
	}
	_, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1"), tc("2", "2")), nil)
	if code(err) != codes.Unavailable || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("expected Unavailable about the removed image, got %v", err)
	}
	if r.Runs() != 1 {
		t.Fatalf("should stop after the first failure, ran %d", r.Runs())
	}
}

func TestVolumeVanishing(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.RunFunc = func(context.Context, sandbox.Spec) (sandbox.Result, error) {
		return sandbox.Result{}, sandbox.ErrVolumeMissing
	}
	_, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1"), tc("2", "2")), nil)
	if code(err) != codes.Internal || r.Runs() != 1 {
		t.Fatalf("expected Internal after one run, got %v after %d runs", err, r.Runs())
	}
}

func TestFileSizeLimitsAreSet(t *testing.T) {
	r := sandboxtest.NewRunner()
	if _, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1")), nil); err != nil {
		t.Fatal(err)
	}
	for _, spec := range r.Specs() {
		if spec.MaxFileBytes <= 0 {
			t.Fatalf("every container needs a file size limit, got %+v", spec)
		}
	}
}

func TestCompileRunnerErrorFailsRequest(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) { return sandbox.Result{}, errors.New("no such image") }
	_, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1")), nil)
	if code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
	if r.LiveVolumes() != 0 {
		t.Fatal("volume leaked")
	}
}

func TestValidation(t *testing.T) {
	big := strings.Repeat("x", 1001)
	cases := []struct {
		name string
		edit func(*pb.VerifyRequest)
		code codes.Code
		want string
	}{
		{"no language", func(r *pb.VerifyRequest) { r.Language = "" }, codes.InvalidArgument, "language"},
		{"unknown language", func(r *pb.VerifyRequest) { r.Language = "cobol" }, codes.InvalidArgument, "cobol"},
		{"disabled language", func(r *pb.VerifyRequest) { r.Language = "rust" }, codes.FailedPrecondition, "rust"},
		{"empty source", func(r *pb.VerifyRequest) { r.SourceCode = "  \n" }, codes.InvalidArgument, "source_code"},
		{"huge source", func(r *pb.VerifyRequest) { r.SourceCode = big }, codes.InvalidArgument, "source_code"},
		{"no tests", func(r *pb.VerifyRequest) { r.TestCases = nil }, codes.InvalidArgument, "test case"},
		{"too many tests", func(r *pb.VerifyRequest) {
			for range 6 {
				r.TestCases = append(r.TestCases, tc("", ""))
			}
		}, codes.InvalidArgument, "at most 5"},
		{"nil test", func(r *pb.VerifyRequest) { r.TestCases = []*pb.TestCase{nil} }, codes.InvalidArgument, "test case 1"},
		{"time too high", func(r *pb.VerifyRequest) { r.TimeLimitMs = 10001 }, codes.InvalidArgument, "time_limit_ms"},
		{"time too low", func(r *pb.VerifyRequest) { r.TimeLimitMs = 50 }, codes.InvalidArgument, "time_limit_ms"},
		{"memory too high", func(r *pb.VerifyRequest) { r.MemoryLimitMb = 2048 }, codes.InvalidArgument, "memory_limit_mb"},
		{"memory too low", func(r *pb.VerifyRequest) { r.MemoryLimitMb = 8 }, codes.InvalidArgument, "memory_limit_mb"},
		{"bad compare mode", func(r *pb.VerifyRequest) { r.CompareMode = 42 }, codes.InvalidArgument, "compare_mode"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := sandboxtest.NewRunner()
			req := request(tc("1", "1"))
			c.edit(req)
			_, err := newService(t, r, nil).Verify(context.Background(), req, nil)
			if code(err) != c.code || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %v mentioning %q", err, c.code, c.want)
			}
			if len(r.Specs()) != 0 {
				t.Fatal("nothing should run for an invalid request")
			}
		})
	}
	if _, err := newService(t, sandboxtest.NewRunner(), nil).Verify(context.Background(), nil, nil); code(err) != codes.InvalidArgument {
		t.Fatalf("nil request should be rejected, got %v", err)
	}
}

func TestImageNotReady(t *testing.T) {
	s := newService(t, sandboxtest.NewRunner(), func(c *Config) {
		c.Images = sandboxtest.Images{NotReady: map[string]bool{"python:3.14-alpine": true}}
	})
	_, err := s.Verify(context.Background(), request(tc("1", "1")), nil)
	if code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

func TestCancelledRequest(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.RunFunc = func(ctx context.Context, _ sandbox.Spec) (sandbox.Result, error) {
		<-ctx.Done()
		return sandbox.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := newService(t, r, nil).Verify(ctx, request(tc("1", "1")), nil)
	if code(err) != codes.Canceled {
		t.Fatalf("expected Canceled, got %v", err)
	}
	if r.LiveVolumes() != 0 {
		t.Fatal("volume leaked after cancel")
	}
}

func TestEventsInOrder(t *testing.T) {
	var kinds []string
	emit := func(ev *pb.VerifyEvent) error {
		switch ev.Event.(type) {
		case *pb.VerifyEvent_Compile:
			kinds = append(kinds, "compile")
		case *pb.VerifyEvent_TestResult:
			kinds = append(kinds, fmt.Sprintf("test%d", ev.GetTestResult().Index))
		case *pb.VerifyEvent_Summary:
			kinds = append(kinds, "summary")
		}
		return nil
	}
	_, err := newService(t, sandboxtest.NewRunner(), nil).Verify(context.Background(), request(tc("1", "1"), tc("2", "2")), emit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(kinds, ",") != "compile,test0,test1,summary" {
		t.Fatalf("unexpected event order %v", kinds)
	}
}

func TestEmitErrorStopsWork(t *testing.T) {
	r := sandboxtest.NewRunner()
	emit := func(ev *pb.VerifyEvent) error {
		if ev.GetTestResult() != nil {
			return status.Error(codes.Unavailable, "client went away")
		}
		return nil
	}
	_, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1"), tc("2", "2"), tc("3", "3")), emit)
	if code(err) != codes.Unavailable {
		t.Fatalf("expected the emit error back, got %v", err)
	}
	if r.Runs() != 1 {
		t.Fatalf("should stop after the failed send, ran %d", r.Runs())
	}
}

func TestQueueFull(t *testing.T) {
	r := sandboxtest.NewRunner()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	r.RunFunc = func(_ context.Context, spec sandbox.Spec) (sandbox.Result, error) {
		started <- struct{}{}
		<-release
		return sandbox.Result{Stdout: spec.Stdin}, nil
	}
	s := newService(t, r, func(c *Config) { c.MaxConcurrent = 1; c.MaxQueue = 0 })

	done := make(chan error)
	go func() {
		_, err := s.Verify(context.Background(), request(tc("1", "1")), nil)
		done <- err
	}()
	<-started
	_, err := s.Verify(context.Background(), request(tc("1", "1")), nil)
	if code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueueWaitsForSlot(t *testing.T) {
	r := sandboxtest.NewRunner()
	s := newService(t, r, func(c *Config) { c.MaxConcurrent = 1; c.MaxQueue = 10 })
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Verify(context.Background(), request(tc("1", "1")), nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestOutputIsSanitizedAndTruncated(t *testing.T) {
	r := sandboxtest.NewRunner()
	r.RunFunc = func(context.Context, sandbox.Spec) (sandbox.Result, error) {
		out := append(bytes.Repeat([]byte("é"), 600), 0xff, 0xfe)
		return sandbox.Result{Stdout: out, Stderr: []byte{0xc3}}, nil
	}
	resp, err := newService(t, r, nil).Verify(context.Background(), request(tc("1", "1")), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := resp.Results[0]
	if !utf8.ValidString(res.Stdout) || !utf8.ValidString(res.Stderr) {
		t.Fatal("output must be valid UTF-8 or protobuf refuses to send it")
	}
	if !res.StdoutTruncated || len(res.Stdout) > 1024 || res.StderrTruncated {
		t.Fatalf("truncation wrong, len %d truncated %v %v", len(res.Stdout), res.StdoutTruncated, res.StderrTruncated)
	}
}

func TestLanguagesList(t *testing.T) {
	s := newService(t, sandboxtest.NewRunner(), func(c *Config) {
		c.Images = sandboxtest.Images{NotReady: map[string]bool{"gcc:15": true}}
	})
	langs := s.Languages(context.Background())
	if len(langs) != 3 {
		t.Fatalf("expected 3 enabled languages, got %d", len(langs))
	}
	for _, l := range langs {
		if l.Id == "cpp" && l.Ready {
			t.Fatal("cpp image is not ready")
		}
		if l.Id == "python" && (!l.Ready || l.SourceFile != "main.py" || len(l.Aliases) == 0) {
			t.Fatalf("python entry is wrong %v", l)
		}
	}
}
