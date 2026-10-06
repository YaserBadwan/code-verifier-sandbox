package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

type printer struct {
	w          io.Writer
	showOutput bool
	numWidth   int
	nameWidth  int
}

// widths are worked out up front so the columns line up while results stream in
func newPrinter(w io.Writer, showOutput bool, tests []*pb.TestCase) *printer {
	p := &printer{w: w, showOutput: showOutput, numWidth: len(fmt.Sprint(len(tests)))}
	for _, tc := range tests {
		p.nameWidth = max(p.nameWidth, len(tc.Name))
	}
	return p
}

// VERDICT_WRONG_ANSWER reads better as WRONG_ANSWER
func verdictName(v pb.Verdict) string {
	return strings.TrimPrefix(v.String(), "VERDICT_")
}

func (p *printer) compile(c *pb.CompileResult) {
	if c.Success {
		fmt.Fprintf(p.w, "compiled in %d ms\n", c.DurationMs)
		if strings.TrimSpace(c.Output) != "" {
			fmt.Fprintln(p.w, indent(c.Output))
		}
		return
	}
	fmt.Fprintf(p.w, "COMPILATION_ERROR after %d ms\n%s\n", c.DurationMs, indent(c.Output))
}

func (p *printer) test(r *pb.TestResult) {
	line := fmt.Sprintf("test %*d", p.numWidth, r.Index+1)
	if p.nameWidth > 0 {
		line += fmt.Sprintf("  %-*s", p.nameWidth, r.Name)
	}
	line += "  " + verdictName(r.Verdict)
	if r.Verdict != pb.Verdict_VERDICT_SKIPPED {
		line += fmt.Sprintf("  %d ms", r.DurationMs)
	}
	if r.Message != "" && r.Verdict != pb.Verdict_VERDICT_SKIPPED {
		line += "  " + r.Message
	}
	fmt.Fprintln(p.w, line)

	failed := r.Verdict != pb.Verdict_VERDICT_ACCEPTED && r.Verdict != pb.Verdict_VERDICT_SKIPPED
	if p.showOutput && failed {
		p.block("stdout", r.Stdout, r.StdoutTruncated)
		p.block("stderr", r.Stderr, r.StderrTruncated)
	}
}

func (p *printer) block(label, text string, truncated bool) {
	if text == "" {
		return
	}
	suffix := ""
	if truncated {
		suffix = " (truncated)"
	}
	fmt.Fprintf(p.w, "  %s%s\n%s\n", label, suffix, indent(strings.TrimRight(text, "\n")))
}

func (p *printer) summary(s *pb.VerifyResponse) {
	took := (time.Duration(s.DurationMs) * time.Millisecond).Round(10 * time.Millisecond)
	fmt.Fprintf(p.w, "\n%s  %d/%d passed in %v  (submission %s)\n", verdictName(s.Verdict), s.Passed, s.Total, took, s.SubmissionId)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}
