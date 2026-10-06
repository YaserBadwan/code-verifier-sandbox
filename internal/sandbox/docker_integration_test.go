//go:build integration

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/dockerhost"
	"github.com/moby/moby/client"
)

const testImage = "python:3.14-alpine"

func newTestDocker(t *testing.T) *Docker {
	t.Helper()
	d := newTestDockerNoImage(t)
	if ok, err := d.ImageExists(context.Background(), testImage); !ok {
		t.Skipf("skipping, %s is not available (run make images): %v", testImage, err)
	}
	return d
}

func newTestDockerNoImage(t *testing.T) *Docker {
	t.Helper()
	host, err := dockerhost.Resolve(os.Getenv)
	if err != nil {
		t.Skipf("skipping, docker host could not be resolved: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := NewDocker(ctx, host, "", quietLog)
	if err != nil {
		t.Skipf("skipping, docker is not available: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// runPython puts code in a fresh volume like the judge does and runs it.
func runPython(t *testing.T, d *Docker, ctx context.Context, code string, mutate func(*Spec)) (Result, error) {
	t.Helper()
	labels := map[string]string{LabelSubmission: "test-" + strings.ReplaceAll(t.Name(), "/", "-")}
	vol, err := d.CreateVolume(context.Background(), labels)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.RemoveVolume(context.Background(), vol) })

	_, err = d.Run(context.Background(), Spec{
		Image: testImage, Cmd: []string{"python", "-m", "py_compile", "main.py"},
		Env:    []string{"PYTHONPYCACHEPREFIX=/tmp/pycache"},
		Volume: vol, WritableVolume: true,
		Files:   []File{{Name: "main.py", Data: []byte(code)}},
		Timeout: 20 * time.Second, MemoryBytes: 256 << 20, NanoCPUs: 1e9, PidsLimit: 64,
		OutputLimit: 64 << 10, TmpfsSize: "64m", MaxFileBytes: 16 << 20, Labels: labels,
	})
	if err != nil {
		t.Fatalf("setup run failed: %v", err)
	}

	spec := Spec{
		Image: testImage, Cmd: []string{"python", "main.py"},
		Env:     []string{"PYTHONDONTWRITEBYTECODE=1"},
		Volume:  vol,
		Timeout: 5 * time.Second, MemoryBytes: 128 << 20, NanoCPUs: 1e9, PidsLimit: 32,
		OutputLimit: 64 << 10, TmpfsSize: "16m", MaxFileBytes: 16 << 20, Labels: labels,
	}
	if mutate != nil {
		mutate(&spec)
	}
	return d.Run(ctx, spec)
}

func TestStdinAndStdout(t *testing.T) {
	d := newTestDocker(t)
	res, err := runPython(t, d, context.Background(), "import sys\nprint('got', sys.stdin.read(), end='')", func(s *Spec) {
		s.Stdin = []byte("hello\nworld")
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Stdout) != "got hello\nworld" || res.ExitCode != 0 {
		t.Fatalf("unexpected result %q exit %d stderr %q", res.Stdout, res.ExitCode, res.Stderr)
	}
}

func TestEmptyStdinGivesEOF(t *testing.T) {
	d := newTestDocker(t)
	res, err := runPython(t, d, context.Background(), "import sys\nprint(len(sys.stdin.read()))", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "0" || res.TimedOut {
		t.Fatalf("program should see EOF right away, got %q timedOut=%v", res.Stdout, res.TimedOut)
	}
}

func TestStderrAndExitCode(t *testing.T) {
	d := newTestDocker(t)
	res, err := runPython(t, d, context.Background(), "import sys\nsys.stderr.write('boom')\nsys.exit(3)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || string(res.Stderr) != "boom" {
		t.Fatalf("got exit %d stderr %q", res.ExitCode, res.Stderr)
	}
}

func TestTimeoutKillsProgram(t *testing.T) {
	d := newTestDocker(t)
	started := time.Now()
	res, err := runPython(t, d, context.Background(), "while True: pass", func(s *Spec) { s.Timeout = time.Second })
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected a timeout")
	}
	if time.Since(started) > 15*time.Second {
		t.Fatalf("timeout took far too long, %v", time.Since(started))
	}
}

func TestOutputFloodIsCut(t *testing.T) {
	d := newTestDocker(t)
	res, err := runPython(t, d, context.Background(), "while True: print('x' * 1000)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OutputLimitExceeded || res.TimedOut {
		t.Fatalf("expected output limit, got %+v", res)
	}
	if len(res.Stdout) != 64<<10 {
		t.Fatalf("expected exactly the limit kept, got %d bytes", len(res.Stdout))
	}
}

func TestStderrFloodIsCut(t *testing.T) {
	d := newTestDocker(t)
	res, err := runPython(t, d, context.Background(), "import sys\nwhile True: sys.stderr.write('e' * 1000)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OutputLimitExceeded {
		t.Fatal("stderr flood should also hit the output limit")
	}
}

func TestMemoryLimit(t *testing.T) {
	d := newTestDocker(t)
	// bytearray touches every page so the memory really gets used
	res, err := runPython(t, d, context.Background(), "chunks = []\nwhile True: chunks.append(bytearray(16 * 1024 * 1024))", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OOMKilled && !bytes.Contains(res.Stderr, []byte("MemoryError")) {
		t.Fatalf("expected the program to run out of memory, got %+v stderr %q", res, res.Stderr)
	}
}

func TestNoNetwork(t *testing.T) {
	d := newTestDocker(t)
	code := "import socket\ntry:\n    socket.create_connection(('1.1.1.1', 80), timeout=2)\n    print('connected')\nexcept OSError as e:\n    print('blocked')"
	res, err := runPython(t, d, context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "blocked" {
		t.Fatalf("network should be unreachable, got %q %q", res.Stdout, res.Stderr)
	}
}

func TestFilesystemIsLockedDown(t *testing.T) {
	d := newTestDocker(t)
	code := `import os
results = []
for path in ['/etc/evil', '/workspace/box/evil', '/usr/evil']:
    try:
        open(path, 'w').write('x')
        results.append('wrote ' + path)
    except OSError:
        results.append('denied')
open('/tmp/ok', 'w').write('fine')
results.append(open('/tmp/ok').read())
results.append(str(os.getuid()))
print(' '.join(results))`
	res, err := runPython(t, d, context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "denied denied denied fine 65534" {
		t.Fatalf("unexpected filesystem access %q %q", res.Stdout, res.Stderr)
	}
}

func TestForkBombIsContained(t *testing.T) {
	d := newTestDocker(t)
	code := `import os
count = 0
try:
    while True:
        if os.fork() == 0:
            import time
            time.sleep(30)
            os._exit(0)
        count += 1
except OSError:
    print('limited at', count)`
	res, err := runPython(t, d, context.Background(), code, func(s *Spec) { s.Timeout = 3 * time.Second })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Stdout), "limited at") {
		t.Fatalf("fork should have been refused by the pids limit, got %q %q", res.Stdout, res.Stderr)
	}
}

func TestBackgroundProcessDiesWithProgram(t *testing.T) {
	d := newTestDocker(t)
	code := "import subprocess\nsubprocess.Popen(['sleep', '60'])\nprint('done')"
	started := time.Now()
	res, err := runPython(t, d, context.Background(), code, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TimedOut || strings.TrimSpace(string(res.Stdout)) != "done" {
		t.Fatalf("leftover child should not keep the test alive, got %+v", res)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatal("took too long, the child process kept the container running")
	}
}

func TestLargeStdinNotRead(t *testing.T) {
	d := newTestDocker(t)
	big := bytes.Repeat([]byte("1234567890\n"), 1<<20)
	res, err := runPython(t, d, context.Background(), "print('ignored input')", func(s *Spec) { s.Stdin = big })
	if err != nil {
		t.Fatal(err)
	}
	if res.TimedOut || strings.TrimSpace(string(res.Stdout)) != "ignored input" {
		t.Fatalf("got %+v", res)
	}
}

func TestLargeStdinRead(t *testing.T) {
	d := newTestDocker(t)
	big := bytes.Repeat([]byte("1234567890\n"), 500000)
	res, err := runPython(t, d, context.Background(), "import sys\nprint(len(sys.stdin.buffer.read()))", func(s *Spec) { s.Stdin = big })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "5500000" {
		t.Fatalf("expected all input to arrive, got %q %q", res.Stdout, res.Stderr)
	}
}

func TestCancelRemovesEverything(t *testing.T) {
	d := newTestDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1500 * time.Millisecond)
		cancel()
	}()
	_, err := runPython(t, d, ctx, "import time\ntime.sleep(60)", func(s *Spec) {
		s.Timeout = 30 * time.Second
		s.Labels[LabelSubmission] = "cancel-test"
	})
	if err == nil {
		t.Fatal("expected a context error")
	}
	list, err := d.cli.ContainerList(context.Background(), client.ContainerListOptions{
		All: true, Filters: make(client.Filters).Add("label", LabelSubmission+"=cancel-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("expected the container to be removed, %d left", len(list.Items))
	}
}

func TestFileSizeLimit(t *testing.T) {
	d := newTestDocker(t)
	code := "try:\n    open('/tmp/big', 'wb').write(b'x' * (12 << 20))\n    print('wrote')\nexcept OSError as e:\n    print('limited')"
	res, err := runPython(t, d, context.Background(), code, func(s *Spec) { s.MaxFileBytes = 4 << 20 })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "limited" && res.ExitCode == 0 {
		t.Fatalf("writing past the file size limit should fail, got %q %q exit %d", res.Stdout, res.Stderr, res.ExitCode)
	}
}

func TestMissingVolumeIsAnError(t *testing.T) {
	d := newTestDocker(t)
	_, err := d.Run(context.Background(), Spec{
		Image: testImage, Cmd: []string{"true"}, Volume: "code-verifier-sandbox-does-not-exist",
		Timeout: time.Second, MemoryBytes: 64 << 20, NanoCPUs: 1e9, PidsLimit: 16,
		OutputLimit: 1024, TmpfsSize: "1m", MaxFileBytes: 1 << 20,
	})
	if !errors.Is(err, ErrVolumeMissing) {
		t.Fatalf("expected ErrVolumeMissing, got %v", err)
	}
	if ok, _ := d.cli.VolumeInspect(context.Background(), "code-verifier-sandbox-does-not-exist", client.VolumeInspectOptions{}); ok.Volume.Name != "" {
		d.RemoveVolume(context.Background(), "code-verifier-sandbox-does-not-exist")
		t.Fatal("the missing volume must not be created on the fly")
	}
}

func TestMissingImageIsTyped(t *testing.T) {
	d := newTestDocker(t)
	_, err := d.Run(context.Background(), Spec{
		Image: "code-verifier-sandbox/not-a-real-image:never", Cmd: []string{"true"},
		Timeout: time.Second, MemoryBytes: 64 << 20, NanoCPUs: 1e9, PidsLimit: 16,
		OutputLimit: 1024, TmpfsSize: "1m", MaxFileBytes: 1 << 20,
	})
	if !errors.Is(err, ErrImageMissing) {
		t.Fatalf("expected ErrImageMissing, got %v", err)
	}
}
