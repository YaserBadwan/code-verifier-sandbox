//go:build integration

package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/client"
	"google.golang.org/grpc/codes"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/dockerhost"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	pb "github.com/YaserBadwan/code-verifier-sandbox/proto/verifier/v1"
)

var (
	dockerOnce sync.Once
	sharedDock *sandbox.Docker
	dockerErr  error
)

func realService(t *testing.T, mutate func(*Config)) (*Service, *sandbox.Docker) {
	t.Helper()
	dockerOnce.Do(func() {
		host, err := dockerhost.Resolve(os.Getenv)
		if err != nil {
			dockerErr = err
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sharedDock, dockerErr = sandbox.NewDocker(ctx, host, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	if dockerErr != nil {
		t.Skipf("skipping, docker is not available: %v", dockerErr)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	langs := languages.All()
	var refs []string
	for _, l := range langs {
		refs = append(refs, l.Image)
	}
	images := sandbox.NewImages(sharedDock, refs, log)
	images.Refresh(context.Background())

	cfg := Config{
		Runner: sharedDock, Images: images, Languages: langs,
		Limits: Limits{
			DefaultTimeLimit: 3 * time.Second, MaxTimeLimit: 15 * time.Second,
			DefaultMemoryMB: 256, MaxMemoryMB: 1024,
			CompileTimeout: 60 * time.Second, CompileMemoryMB: 1024,
			OutputLimitBytes: 256 << 10, ReportBytes: 64 << 10,
			MaxSourceBytes: 256 << 10, MaxTests: 100,
		},
		MaxConcurrent: 4, MaxQueue: 32, Log: log,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg), sharedDock
}

func needImage(t *testing.T, s *Service, lang string) {
	t.Helper()
	l, _ := languages.Lookup(lang)
	if !s.images.Ready(context.Background(), l.Image) {
		t.Skipf("skipping, %s is not pulled (run make images)", l.Image)
	}
}

func exampleTests(t *testing.T) []*pb.TestCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "sum", "tests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw []struct {
		Name           string `json:"name"`
		Input          string `json:"input"`
		ExpectedOutput string `json:"expected_output"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var out []*pb.TestCase
	for _, r := range raw {
		out = append(out, &pb.TestCase{Name: r.Name, Input: r.Input, ExpectedOutput: r.ExpectedOutput})
	}
	return out
}

var exampleFiles = map[string]string{
	"c": "solution.c", "cpp": "solution.cpp", "go": "solution.go", "java": "Main.java",
	"javascript": "solution.js", "python": "solution.py", "rust": "solution.rs",
}

// broken programs per language, each should produce the given verdict
var brokenPrograms = map[string]map[pb.Verdict]string{
	"c": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "int main( { return 0; }",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "int main(void) { int *p = 0; *p = 1; return 0; }",
	},
	"cpp": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "int main() { undefined_thing(); }",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "#include <stdexcept>\nint main() { throw std::runtime_error(\"bad\"); }",
	},
	"go": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "package main\nfunc main() { x := 1 }",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "package main\nfunc main() { var m map[string]int; m[\"a\"] = 1 }",
	},
	"java": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "public class Main { public static void main(String[] a) { int x = \"s\"; } }",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "public class Main { public static void main(String[] a) { throw new RuntimeException(\"bad\"); } }",
	},
	"javascript": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "const = 5;",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "null.x;",
	},
	"python": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "def broken(:\n  pass",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "raise ValueError('bad')",
	},
	"rust": {
		pb.Verdict_VERDICT_COMPILATION_ERROR: "fn main() { let x: i32 = \"s\"; }",
		pb.Verdict_VERDICT_RUNTIME_ERROR:     "fn main() { let v: Vec<i32> = vec![]; println!(\"{}\", v[3]); }",
	},
}

func TestEveryLanguage(t *testing.T) {
	s, _ := realService(t, nil)
	tests := exampleTests(t)
	for lang, file := range exampleFiles {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			needImage(t, s, lang)
			source, err := os.ReadFile(filepath.Join("..", "..", "examples", "sum", file))
			if err != nil {
				t.Fatal(err)
			}

			resp, err := s.Verify(context.Background(), &pb.VerifyRequest{Language: lang, SourceCode: string(source), TestCases: tests}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict != pb.Verdict_VERDICT_ACCEPTED || int(resp.Passed) != len(tests) {
				t.Fatalf("example should pass, got %v compile %q results %v", resp.Verdict, resp.Compile.Output, resp.Results)
			}

			wrong := []*pb.TestCase{{Input: "1 2\n", ExpectedOutput: "4\n"}}
			resp, err = s.Verify(context.Background(), &pb.VerifyRequest{Language: lang, SourceCode: string(source), TestCases: wrong}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict != pb.Verdict_VERDICT_WRONG_ANSWER {
				t.Fatalf("expected wrong answer, got %v", resp.Verdict)
			}

			for want, code := range brokenPrograms[lang] {
				resp, err := s.Verify(context.Background(), &pb.VerifyRequest{Language: lang, SourceCode: code, TestCases: wrong}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if resp.Verdict != want {
					t.Fatalf("%q should give %v, got %v compile %q results %v", code, want, resp.Verdict, resp.Compile.Output, resp.Results)
				}
				if want == pb.Verdict_VERDICT_COMPILATION_ERROR && strings.TrimSpace(resp.Compile.Output) == "" {
					t.Fatal("compilation error should include the compiler output")
				}
			}
		})
	}
}

type attack struct {
	name    string
	lang    string
	code    string
	input   string
	expect  string
	verdict pb.Verdict
	message string
	// checked against the compiler output when set
	compileOutput string
	edit          func(*pb.VerifyRequest)
}

func TestAttacksAndEdgeCases(t *testing.T) {
	s, _ := realService(t, nil)
	cases := []attack{
		{name: "infinite loop python", lang: "python", code: "while True: pass", verdict: pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED,
			edit: func(r *pb.VerifyRequest) { r.TimeLimitMs = 1000 }},
		{name: "infinite loop c", lang: "c", code: "int main(void) { for (;;) {} }", verdict: pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED,
			edit: func(r *pb.VerifyRequest) { r.TimeLimitMs = 1000 }},
		{name: "sleep forever", lang: "python", code: "import time\ntime.sleep(3600)", verdict: pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED,
			edit: func(r *pb.VerifyRequest) { r.TimeLimitMs = 500 }},
		{name: "memory hog python", lang: "python", code: "x = []\nwhile True: x.append(bytearray(10 << 20))", verdict: pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED,
			edit: func(r *pb.VerifyRequest) { r.MemoryLimitMb = 64 }},
		{name: "memory hog c", lang: "c", code: "#include <stdlib.h>\n#include <string.h>\nint main(void) { for (;;) { char *p = malloc(1 << 24); if (!p) return 3; memset(p, 1, 1 << 24); } }",
			verdict: pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED, edit: func(r *pb.VerifyRequest) { r.MemoryLimitMb = 64 }},
		{name: "memory hog java", lang: "java", code: "import java.util.*;\npublic class Main { public static void main(String[] a) { List<long[]> l = new ArrayList<>(); while (true) l.add(new long[1 << 20]); } }",
			verdict: pb.Verdict_VERDICT_MEMORY_LIMIT_EXCEEDED, edit: func(r *pb.VerifyRequest) { r.MemoryLimitMb = 128 }},
		{name: "output flood", lang: "python", code: "while True: print('spam' * 100)", verdict: pb.Verdict_VERDICT_OUTPUT_LIMIT_EXCEEDED},
		{name: "fork bomb", lang: "c", code: "#include <unistd.h>\nint main(void) { for (;;) fork(); }", verdict: pb.Verdict_VERDICT_TIME_LIMIT_EXCEEDED,
			edit: func(r *pb.VerifyRequest) { r.TimeLimitMs = 1500 }},
		{name: "segfault", lang: "c", code: "int main(void) { int *p = 0; return *p; }", verdict: pb.Verdict_VERDICT_RUNTIME_ERROR, message: "SIGSEGV"},
		// ARM cpus return 0 for integer division by zero instead of trapping, so raise it directly
		{name: "arithmetic signal", lang: "cpp", code: "#include <csignal>\nint main() { std::raise(SIGFPE); }", verdict: pb.Verdict_VERDICT_RUNTIME_ERROR, message: "SIGFPE"},
		{name: "abort", lang: "c", code: "#include <stdlib.h>\nint main(void) { abort(); }", verdict: pb.Verdict_VERDICT_RUNTIME_ERROR, message: "SIGABRT"},
		{name: "exit code", lang: "python", code: "import sys\nsys.exit(42)", verdict: pb.Verdict_VERDICT_RUNTIME_ERROR, message: "code 42"},
		{name: "stack overflow java", lang: "java", code: "public class Main { static int f(int n) { return f(n + 1) + 1; } public static void main(String[] a) { f(0); } }",
			verdict: pb.Verdict_VERDICT_RUNTIME_ERROR},
		{name: "network is off", lang: "python", expect: "blocked\n", verdict: pb.Verdict_VERDICT_ACCEPTED,
			code: "import socket\ntry:\n    socket.create_connection(('8.8.8.8', 53), timeout=2)\n    print('open')\nexcept OSError:\n    print('blocked')"},
		{name: "dns is off", lang: "python", expect: "blocked\n", verdict: pb.Verdict_VERDICT_ACCEPTED,
			code: "import socket\ntry:\n    socket.gethostbyname('example.com')\n    print('open')\nexcept OSError:\n    print('blocked')"},
		{name: "cannot write outside tmp", lang: "python", expect: "denied\n", verdict: pb.Verdict_VERDICT_ACCEPTED,
			code: "try:\n    open('/workspace/box/main.py', 'w').write('hacked')\n    print('wrote')\nexcept OSError:\n    print('denied')"},
		{name: "runs as nobody", lang: "python", code: "import os\nprint(os.getuid(), os.getgid())", expect: "65534 65534\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "no capabilities", lang: "python", expect: "0000000000000000\n", verdict: pb.Verdict_VERDICT_ACCEPTED,
			code: "for line in open('/proc/self/status'):\n    if line.startswith('CapEff'):\n        print(line.split()[1])"},
		{name: "no trailing newline", lang: "python", code: "print(input(), end='')", input: "hi", expect: "hi\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "exact mode is strict", lang: "python", code: "print(input(), end='')", input: "hi", expect: "hi\n", verdict: pb.Verdict_VERDICT_WRONG_ANSWER,
			edit: func(r *pb.VerifyRequest) { r.CompareMode = pb.CompareMode_COMPARE_MODE_EXACT }},
		{name: "tokens mode", lang: "python", code: "print(*input().split(), sep='\\n')", input: "1 2 3", expect: "1 2 3", verdict: pb.Verdict_VERDICT_ACCEPTED,
			edit: func(r *pb.VerifyRequest) { r.CompareMode = pb.CompareMode_COMPARE_MODE_TOKENS }},
		{name: "unicode", lang: "javascript", code: "console.log(require('fs').readFileSync(0, 'utf8').trim() + ' ✓')", input: "héllo 世界", expect: "héllo 世界 ✓\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "invalid utf8 output", lang: "python", code: "import sys\nsys.stdout.buffer.write(b'\\xff\\xfe ok')", expect: "x", verdict: pb.Verdict_VERDICT_WRONG_ANSWER},
		{name: "empty input", lang: "python", code: "import sys\nprint(len(sys.stdin.read()))", expect: "0\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "stdin never read", lang: "c", code: "#include <stdio.h>\nint main(void) { puts(\"ok\"); return 0; }", input: strings.Repeat("9", 4<<20), expect: "ok\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "large stdin", lang: "cpp", code: "#include <iostream>\n#include <string>\nint main() { std::string s; long long n = 0; while (std::cin >> s) n += s.size(); std::cout << n << '\\n'; }",
			input: strings.Repeat("abcdefghij ", 400000), expect: "4000000\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "background process", lang: "python", code: "import subprocess\nsubprocess.Popen(['sleep', '100'])\nprint('done')", expect: "done\n", verdict: pb.Verdict_VERDICT_ACCEPTED},
		{name: "java wrong class name", lang: "java", code: "public class Solution { public static void main(String[] a) {} }", verdict: pb.Verdict_VERDICT_COMPILATION_ERROR, compileOutput: "must be named Main"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			needImage(t, s, c.lang)
			req := &pb.VerifyRequest{Language: c.lang, SourceCode: c.code, TestCases: []*pb.TestCase{{Input: c.input, ExpectedOutput: c.expect}}}
			if c.edit != nil {
				c.edit(req)
			}
			started := time.Now()
			resp, err := s.Verify(context.Background(), req, nil)
			if err != nil {
				t.Fatal(err)
			}
			res := resp.Results[0]
			if resp.Verdict != c.verdict {
				t.Fatalf("got %v (%s), want %v\ncompile %q\nstdout %q\nstderr %q", resp.Verdict, res.Message, c.verdict, resp.Compile.Output, res.Stdout, res.Stderr)
			}
			if c.message != "" && !strings.Contains(res.Message, c.message) {
				t.Fatalf("message %q should contain %q", res.Message, c.message)
			}
			if c.compileOutput != "" && !strings.Contains(resp.Compile.Output, c.compileOutput) {
				t.Fatalf("compile output %q should contain %q", resp.Compile.Output, c.compileOutput)
			}
			if !utf8.ValidString(res.Stdout) || !utf8.ValidString(res.Stderr) {
				t.Fatal("output must always be valid UTF-8")
			}
			if took := time.Since(started); took > 45*time.Second {
				t.Fatalf("took %v, something is not being killed", took)
			}
		})
	}
}

func TestCompileBombIsStopped(t *testing.T) {
	s, _ := realService(t, func(c *Config) { c.Limits.CompileTimeout = 5 * time.Second })
	needImage(t, s, "cpp")
	// each level doubles the work for the compiler
	code := `template<int N> struct A { using T = typename A<N-1>::T; static const int v = A<N-1>::v + A<N-1>::v; };
template<> struct A<0> { using T = int; static const int v = 1; };
int main() { return A<100000>::v; }`
	started := time.Now()
	resp, err := s.Verify(context.Background(), &pb.VerifyRequest{Language: "cpp", SourceCode: code, TestCases: []*pb.TestCase{{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_COMPILATION_ERROR {
		t.Fatalf("expected compilation error, got %v", resp.Verdict)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("compile limits were not enforced")
	}
}

func TestLinkerDiskBombIsStopped(t *testing.T) {
	s, _ := realService(t, nil)
	needImage(t, s, "c")
	// every section is aligned to 256 MB, so without a file size limit the linker
	// writes a binary of about 2 GB into the workspace volume
	var b strings.Builder
	for i := range 8 {
		fmt.Fprintf(&b, "char a%d __attribute__((section(\".data.s%d\"), aligned(1 << 28))) = 1;\n", i, i)
	}
	b.WriteString("int main(void) { return a0 + a7; }\n")
	started := time.Now()
	resp, err := s.Verify(context.Background(), &pb.VerifyRequest{Language: "c", SourceCode: b.String(), TestCases: []*pb.TestCase{{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Verdict != pb.Verdict_VERDICT_COMPILATION_ERROR {
		t.Fatalf("expected the oversized binary to fail compiling, got %v %q", resp.Verdict, resp.Compile.Output)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("took too long")
	}
}

func TestStopOnFirstFailureReal(t *testing.T) {
	s, _ := realService(t, nil)
	needImage(t, s, "python")
	req := &pb.VerifyRequest{
		Language: "python", SourceCode: "print(int(input()) * 2)", StopOnFirstFailure: true,
		TestCases: []*pb.TestCase{{Input: "1", ExpectedOutput: "2"}, {Input: "x", ExpectedOutput: "?"}, {Input: "3", ExpectedOutput: "6"}},
	}
	resp, err := s.Verify(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(resp.Results[0].Verdict, resp.Results[1].Verdict, resp.Results[2].Verdict)
	want := fmt.Sprint(pb.Verdict_VERDICT_ACCEPTED, pb.Verdict_VERDICT_RUNTIME_ERROR, pb.Verdict_VERDICT_SKIPPED)
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if !strings.Contains(resp.Results[1].Stderr, "ValueError") {
		t.Fatalf("stderr should be reported, got %q", resp.Results[1].Stderr)
	}
}

func TestConcurrentSubmissions(t *testing.T) {
	s, _ := realService(t, func(c *Config) { c.MaxConcurrent = 3 })
	needImage(t, s, "python")
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := fmt.Sprint(i)
			resp, err := s.Verify(context.Background(), &pb.VerifyRequest{
				Language: "python", SourceCode: "print(input())",
				TestCases: []*pb.TestCase{{Input: in, ExpectedOutput: in}, {Input: in + in, ExpectedOutput: in + in}},
			}, nil)
			if err != nil {
				errs <- err
				return
			}
			if resp.Verdict != pb.Verdict_VERDICT_ACCEPTED {
				errs <- fmt.Errorf("submission %d got %v", i, resp.Verdict)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCancelLeavesNothingBehind(t *testing.T) {
	s, d := realService(t, nil)
	needImage(t, s, "python")
	ctx, cancel := context.WithCancel(context.Background())
	emit := func(ev *pb.VerifyEvent) error {
		if ev.GetCompile() != nil {
			go func() {
				time.Sleep(500 * time.Millisecond)
				cancel()
			}()
		}
		return nil
	}
	_, err := s.Verify(ctx, &pb.VerifyRequest{
		Language: "python", SourceCode: "import time\ntime.sleep(60)", TimeLimitMs: 10000,
		TestCases: []*pb.TestCase{{}, {}, {}},
	}, emit)
	if code(err) != codes.Canceled {
		t.Fatalf("expected Canceled, got %v", err)
	}

	// give the deferred cleanup a moment, it runs on a fresh context
	deadline := time.Now().Add(10 * time.Second)
	for {
		containers, volumes := countManaged(t, d)
		if containers == 0 && volumes == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("leftovers after cancel, %d containers and %d volumes", containers, volumes)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// counts only resources that do not belong to tests still running in parallel
func countManaged(t *testing.T, d *sandbox.Docker) (int, int) {
	t.Helper()
	cli, err := client.New(client.FromEnv, client.WithHost(d.Host()))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	filter := make(client.Filters).Add("label", sandbox.LabelManaged+"=true")
	list, err := cli.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		t.Fatal(err)
	}
	vols, err := cli.VolumeList(context.Background(), client.VolumeListOptions{Filters: filter})
	if err != nil {
		t.Fatal(err)
	}
	return len(list.Items), len(vols.Items)
}
