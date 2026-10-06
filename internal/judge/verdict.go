package judge

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/compare"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

var signalNames = map[int]string{
	4:  "SIGILL (illegal instruction)",
	6:  "SIGABRT (aborted)",
	7:  "SIGBUS (bus error)",
	8:  "SIGFPE (arithmetic error, often division by zero)",
	9:  "SIGKILL",
	11: "SIGSEGV (segmentation fault)",
	15: "SIGTERM",
}

func classify(job *job, res sandbox.Result, expected []byte) (pb.Verdict, string) {
	switch {
	case res.TimedOut:
		return pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED, fmt.Sprintf("exceeded the time limit of %v", job.timeLimit)
	case res.OutputLimitExceeded:
		return pb.Verdict_VERDICT_OUTPUT_LIMIT_EXCEEDED, "wrote more output than the server allows"
	case res.OOMKilled || (res.ExitCode != 0 && containsAny(res.Stderr, job.lang.OOMMarkers...)):
		return pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED, fmt.Sprintf("exceeded the memory limit of %d MB", job.memoryMB)
	case res.ExitCode > 128 && signalNames[res.ExitCode-128] != "":
		return pb.Verdict_VERDICT_RUNTIME_ERROR, "killed by " + signalNames[res.ExitCode-128]
	case res.ExitCode != 0:
		return pb.Verdict_VERDICT_RUNTIME_ERROR, fmt.Sprintf("exited with code %d", res.ExitCode)
	}
	if diff := compare.FirstDifference(job.mode, expected, res.Stdout); diff != "" {
		return pb.Verdict_VERDICT_WRONG_ANSWER, "wrong answer, " + diff
	}
	return pb.Verdict_VERDICT_ACCEPTED, ""
}

func containsAny(haystack []byte, needles ...string) bool {
	for _, n := range needles {
		if bytes.Contains(haystack, []byte(n)) {
			return true
		}
	}
	return false
}

// truncateUTF8 cuts b to at most limit bytes without splitting a character and
// replaces invalid bytes, since protobuf strings must be valid UTF-8.
func truncateUTF8(b []byte, limit int) (string, bool) {
	cut := false
	if len(b) > limit {
		b = b[:limit]
		cut = true
		// back off up to 3 bytes so we don't end in the middle of a rune
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
			if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
				break
			}
			b = b[:len(b)-1]
		}
	}
	s := strings.ToValidUTF8(string(b), "�")
	for len(s) > limit {
		// the replacement char is 3 bytes so very broken input can grow a little
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
		cut = true
	}
	return s, cut
}

func joinLines(a, b string) string {
	if a == "" {
		return b
	}
	return strings.TrimRight(a, "\n") + "\n" + b
}
