// Package sandboxtest has a fake sandbox for tests that should not need Docker.
package sandboxtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
)

// Runner pretends to be Docker. By default compiling works and running echoes stdin.
type Runner struct {
	// replace the default behaviour when set
	CompileFunc func(sandbox.Spec) (sandbox.Result, error)
	RunFunc     func(context.Context, sandbox.Spec) (sandbox.Result, error)

	mu      sync.Mutex
	live    map[string]bool
	nextVol int
	specs   []sandbox.Spec
	runs    int
}

var _ sandbox.Runner = (*Runner)(nil)

func NewRunner() *Runner {
	return &Runner{live: map[string]bool{}}
}

func (f *Runner) CreateVolume(_ context.Context, _ map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextVol++
	name := fmt.Sprintf("vol-%d", f.nextVol)
	f.live[name] = true
	return name, nil
}

func (f *Runner) RemoveVolume(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, name)
	return nil
}

// the compile step is the only one with a writable volume, that is how the fake tells them apart
func (f *Runner) Run(ctx context.Context, spec sandbox.Spec) (sandbox.Result, error) {
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	compile, run := f.CompileFunc, f.RunFunc
	if !spec.WritableVolume {
		f.runs++
	}
	f.mu.Unlock()

	if spec.WritableVolume {
		if compile != nil {
			return compile(spec)
		}
		return sandbox.Result{}, nil
	}
	if run != nil {
		return run(ctx, spec)
	}
	return sandbox.Result{Stdout: spec.Stdin}, nil
}

func (f *Runner) LiveVolumes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

func (f *Runner) Runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func (f *Runner) Specs() []sandbox.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sandbox.Spec(nil), f.specs...)
}

func (f *Runner) RunSpecs() []sandbox.Spec {
	var out []sandbox.Spec
	for _, s := range f.Specs() {
		if !s.WritableVolume {
			out = append(out, s)
		}
	}
	return out
}

// Images reports every image as ready unless it is listed in NotReady.
type Images struct {
	NotReady map[string]bool
}

func (i Images) Ready(_ context.Context, ref string) bool { return !i.NotReady[ref] }

func (i Images) MarkMissing(string) {}
