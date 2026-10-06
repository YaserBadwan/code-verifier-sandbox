// Package judge compiles a submission once and checks it against every test case.
package judge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/compare"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

type ImageChecker interface {
	Ready(ctx context.Context, ref string) bool
	// MarkMissing is called when Docker says the image is gone after all
	MarkMissing(ref string)
}

type Limits struct {
	DefaultTimeLimit time.Duration
	MaxTimeLimit     time.Duration
	DefaultMemoryMB  int
	MaxMemoryMB      int
	CompileTimeout   time.Duration
	CompileMemoryMB  int
	OutputLimitBytes int64
	// how much of stdout and stderr is sent back per test
	ReportBytes    int
	MaxSourceBytes int
	MaxTests       int
}

type Config struct {
	Runner        sandbox.Runner
	Images        ImageChecker
	Languages     []languages.Language
	Limits        Limits
	MaxConcurrent int
	MaxQueue      int
	Log           *slog.Logger
}

// EmitFunc receives progress events. Returning an error aborts the submission.
type EmitFunc func(*pb.VerifyEvent) error

type Service struct {
	runner sandbox.Runner
	images ImageChecker
	langs  []languages.Language
	limits Limits
	slots  *slots
	log    *slog.Logger
}

// the compile step gets more room than a test run, rustc and javac are hungry
const (
	compileCPUs        = 2
	compilePids        = 512
	compileTmpfs       = "512m"
	compileOutputLimit = 256 << 10
	// no real program needs a bigger binary, but a crafted one can make the linker write gigabytes
	compileMaxFile = 256 << 20
	runPids        = 128
	runTmpfs       = "64m"
	runMaxFile     = 64 << 20
)

func New(cfg Config) *Service {
	return &Service{
		runner: cfg.Runner,
		images: cfg.Images,
		langs:  cfg.Languages,
		limits: cfg.Limits,
		slots:  newSlots(cfg.MaxConcurrent, cfg.MaxQueue),
		log:    cfg.Log,
	}
}

func (s *Service) Languages(ctx context.Context) []*pb.Language {
	out := make([]*pb.Language, 0, len(s.langs))
	for _, l := range s.langs {
		out = append(out, &pb.Language{
			Id:         l.ID,
			Name:       l.Name,
			Aliases:    l.Aliases,
			Image:      l.Image,
			SourceFile: l.SourceFile,
			Ready:      s.images.Ready(ctx, l.Image),
		})
	}
	return out
}

func (s *Service) Verify(ctx context.Context, req *pb.VerifyRequest, emit EmitFunc) (*pb.VerifyResponse, error) {
	if emit == nil {
		emit = func(*pb.VerifyEvent) error { return nil }
	}
	job, err := s.validate(req)
	if err != nil {
		return nil, err
	}
	if !s.images.Ready(ctx, job.lang.Image) {
		return nil, status.Errorf(codes.Unavailable, "the %s image (%s) is not available yet, it is probably still downloading, try again in a moment", job.lang.ID, job.lang.Image)
	}

	if err := s.slots.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.slots.release()

	id := newID()
	log := s.log.With("submission", id, "language", job.lang.ID)
	started := time.Now()

	resp, err := s.judge(ctx, id, job, emit, log)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			log.Info("submission cancelled", "reason", ctxErr)
			return nil, status.FromContextError(ctxErr).Err()
		}
		if _, ok := status.FromError(err); !ok {
			log.Error("submission failed", "error", err)
			err = status.Error(codes.Internal, "the sandbox failed to run this submission, check the server logs")
		}
		return nil, err
	}

	resp.DurationMs = millis(time.Since(started))
	log.Info("submission finished", "verdict", resp.Verdict.String(), "passed", resp.Passed, "total", resp.Total, "took_ms", resp.DurationMs)
	if err := emit(&pb.VerifyEvent{Event: &pb.VerifyEvent_Summary{Summary: resp}}); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Service) judge(ctx context.Context, id string, job *job, emit EmitFunc, log *slog.Logger) (*pb.VerifyResponse, error) {
	labels := map[string]string{sandbox.LabelSubmission: id}
	volume, err := s.runner.CreateVolume(ctx, labels)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := s.runner.RemoveVolume(context.WithoutCancel(ctx), volume); err != nil {
			log.Warn("could not remove workspace volume", "error", err)
		}
	}()

	resp := &pb.VerifyResponse{SubmissionId: id, Total: uint32(len(job.tests))}

	compiled, err := s.runner.Run(ctx, sandbox.Spec{
		Image:          job.lang.Image,
		Cmd:            job.lang.Compile,
		Env:            job.lang.CompileEnv,
		Volume:         volume,
		WritableVolume: true,
		Files:          []sandbox.File{{Name: job.lang.SourceFile, Data: []byte(job.source)}},
		Timeout:        s.limits.CompileTimeout,
		MemoryBytes:    int64(s.limits.CompileMemoryMB) << 20,
		NanoCPUs:       compileCPUs * 1e9,
		PidsLimit:      compilePids,
		OutputLimit:    compileOutputLimit,
		TmpfsSize:      compileTmpfs,
		TmpfsExec:      true,
		MaxFileBytes:   compileMaxFile,
		Labels:         labels,
	})
	if err != nil {
		return nil, s.sandboxError(job, err)
	}
	resp.Compile = s.compileResult(job.lang, compiled)
	if err := emit(&pb.VerifyEvent{Event: &pb.VerifyEvent_Compile{Compile: resp.Compile}}); err != nil {
		return nil, err
	}

	skipReason := ""
	if !resp.Compile.Success {
		resp.Verdict = pb.Verdict_VERDICT_COMPILATION_ERROR
		skipReason = "not run because compilation failed"
	}

	for i, tc := range job.tests {
		var result *pb.TestResult
		if skipReason != "" {
			result = &pb.TestResult{Verdict: pb.Verdict_VERDICT_SKIPPED, Message: skipReason}
		} else {
			result, err = s.runTest(ctx, job, volume, labels, tc, log)
			if err != nil {
				return nil, s.sandboxError(job, err)
			}
		}
		result.Index = uint32(i)
		result.Name = tc.GetName()
		resp.Results = append(resp.Results, result)

		switch result.Verdict {
		case pb.Verdict_VERDICT_ACCEPTED:
			resp.Passed++
		case pb.Verdict_VERDICT_SKIPPED:
		default:
			if resp.Verdict == pb.Verdict_VERDICT_UNSPECIFIED {
				resp.Verdict = result.Verdict
			}
			if job.stopOnFailure {
				skipReason = "not run because an earlier test failed"
			}
		}
		if err := emit(&pb.VerifyEvent{Event: &pb.VerifyEvent_TestResult{TestResult: result}}); err != nil {
			return nil, err
		}
	}

	if resp.Verdict == pb.Verdict_VERDICT_UNSPECIFIED {
		resp.Verdict = pb.Verdict_VERDICT_ACCEPTED
	}
	return resp, nil
}

func (s *Service) runTest(ctx context.Context, job *job, volume string, labels map[string]string, tc *pb.TestCase, log *slog.Logger) (*pb.TestResult, error) {
	res, err := s.runner.Run(ctx, sandbox.Spec{
		Image:        job.lang.Image,
		Cmd:          job.lang.RunCommand(job.memoryMB),
		Env:          job.lang.RunEnv,
		Volume:       volume,
		Stdin:        []byte(tc.GetInput()),
		Timeout:      job.timeLimit,
		MemoryBytes:  int64(job.memoryMB) << 20,
		NanoCPUs:     1e9,
		PidsLimit:    runPids,
		OutputLimit:  s.limits.OutputLimitBytes,
		TmpfsSize:    runTmpfs,
		MaxFileBytes: runMaxFile,
		Labels:       labels,
	})
	if err != nil {
		// these break every remaining test too, so stop the whole submission
		if ctx.Err() != nil || errors.Is(err, sandbox.ErrImageMissing) || errors.Is(err, sandbox.ErrVolumeMissing) {
			return nil, err
		}
		// one broken container should not throw away the results of the other tests
		log.Error("test run failed inside the sandbox", "error", err)
		return &pb.TestResult{Verdict: pb.Verdict_VERDICT_INTERNAL_ERROR, Message: "the sandbox failed to run this test, check the server logs"}, nil
	}

	verdict, message := classify(job, res, []byte(tc.GetExpectedOutput()))
	stdout, stdoutCut := truncateUTF8(res.Stdout, s.limits.ReportBytes)
	stderr, stderrCut := truncateUTF8(res.Stderr, s.limits.ReportBytes)
	return &pb.TestResult{
		Verdict:         verdict,
		Message:         message,
		Stdout:          stdout,
		Stderr:          stderr,
		StdoutTruncated: stdoutCut || (res.OutputLimitExceeded && len(res.Stdout) >= int(s.limits.OutputLimitBytes)),
		StderrTruncated: stderrCut || (res.OutputLimitExceeded && len(res.Stderr) >= int(s.limits.OutputLimitBytes)),
		ExitCode:        int32(res.ExitCode),
		DurationMs:      millis(res.Duration),
	}, nil
}

// sandboxError turns a missing image into a clear error the client can act on
func (s *Service) sandboxError(job *job, err error) error {
	if errors.Is(err, sandbox.ErrImageMissing) {
		s.images.MarkMissing(job.lang.Image)
		return status.Errorf(codes.Unavailable, "the %s image (%s) was removed from Docker, it will be downloaded again if pulling is on, otherwise run make images", job.lang.ID, job.lang.Image)
	}
	return err
}

func (s *Service) compileResult(lang languages.Language, res sandbox.Result) *pb.CompileResult {
	out := res.Stdout
	if len(out) > 0 && len(res.Stderr) > 0 {
		out = append(append(out, '\n'), res.Stderr...)
	} else if len(res.Stderr) > 0 {
		out = res.Stderr
	}
	text, _ := truncateUTF8(out, compileOutputLimit)

	switch {
	case res.TimedOut:
		text = joinLines(text, "compilation took longer than "+s.limits.CompileTimeout.String())
	case res.OOMKilled:
		text = joinLines(text, "the compiler ran out of memory")
	case res.OutputLimitExceeded:
		text = joinLines(text, "the compiler produced too much output")
	case res.ExitCode != 0 && lang.ID == "java" && containsAny(out, "should be declared in a file named"):
		text = joinLines(text, "hint: the public class must be named Main")
	}
	return &pb.CompileResult{
		Success:    res.ExitCode == 0 && !res.TimedOut && !res.OOMKilled && !res.OutputLimitExceeded,
		Output:     text,
		DurationMs: millis(res.Duration),
	}
}

func modeFromProto(m pb.CompareMode) compare.Mode {
	switch m {
	case pb.CompareMode_COMPARE_MODE_EXACT:
		return compare.Exact
	case pb.CompareMode_COMPARE_MODE_TOKENS:
		return compare.Tokens
	default:
		return compare.TrailingWhitespace
	}
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return time.Now().Format("20060102150405.000000")
	}
	return hex.EncodeToString(b)
}

func millis(d time.Duration) uint32 {
	if d < 0 {
		return 0
	}
	return uint32(d.Milliseconds())
}
