package judge

import (
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/compare"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

// minimum per test time limit, anything lower is eaten by process startup
const minTimeLimit = 100 * time.Millisecond

const minMemoryMB = 32

// job is a request that passed validation with every default filled in
type job struct {
	lang          languages.Language
	source        string
	tests         []*pb.TestCase
	timeLimit     time.Duration
	memoryMB      int
	mode          compare.Mode
	stopOnFailure bool
}

func invalid(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

func (s *Service) validate(req *pb.VerifyRequest) (*job, error) {
	if req == nil {
		return nil, invalid("request is empty")
	}
	if strings.TrimSpace(req.Language) == "" {
		return nil, invalid("language is required, call ListLanguages to see the options")
	}
	lang, ok := languages.Lookup(req.Language)
	if !ok {
		return nil, invalid("unsupported language %q, call ListLanguages to see the options", req.Language)
	}
	enabled := false
	for _, l := range s.langs {
		if l.ID == lang.ID {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, status.Errorf(codes.FailedPrecondition, "language %q is disabled on this server", lang.ID)
	}

	if strings.TrimSpace(req.SourceCode) == "" {
		return nil, invalid("source_code is empty")
	}
	if len(req.SourceCode) > s.limits.MaxSourceBytes {
		return nil, invalid("source_code is %d bytes, the limit is %d", len(req.SourceCode), s.limits.MaxSourceBytes)
	}

	if len(req.TestCases) == 0 {
		return nil, invalid("at least one test case is required")
	}
	if len(req.TestCases) > s.limits.MaxTests {
		return nil, invalid("got %d test cases, at most %d are allowed", len(req.TestCases), s.limits.MaxTests)
	}
	for i, tc := range req.TestCases {
		if tc == nil {
			return nil, invalid("test case %d is empty", i+1)
		}
	}

	timeLimit := s.limits.DefaultTimeLimit
	if req.TimeLimitMs != 0 {
		timeLimit = time.Duration(req.TimeLimitMs) * time.Millisecond
		if timeLimit < minTimeLimit || timeLimit > s.limits.MaxTimeLimit {
			return nil, invalid("time_limit_ms must be between %d and %d", minTimeLimit.Milliseconds(), s.limits.MaxTimeLimit.Milliseconds())
		}
	}

	memoryMB := s.limits.DefaultMemoryMB
	if req.MemoryLimitMb != 0 {
		memoryMB = int(req.MemoryLimitMb)
		if memoryMB < minMemoryMB || memoryMB > s.limits.MaxMemoryMB {
			return nil, invalid("memory_limit_mb must be between %d and %d", minMemoryMB, s.limits.MaxMemoryMB)
		}
	}

	if _, known := pb.CompareMode_name[int32(req.CompareMode)]; !known {
		return nil, invalid("compare_mode %d is not a known mode", req.CompareMode)
	}

	return &job{
		lang:          lang,
		source:        req.SourceCode,
		tests:         req.TestCases,
		timeLimit:     timeLimit,
		memoryMB:      memoryMB,
		mode:          modeFromProto(req.CompareMode),
		stopOnFailure: req.StopOnFirstFailure,
	}, nil
}
