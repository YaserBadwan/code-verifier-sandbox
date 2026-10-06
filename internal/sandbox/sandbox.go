// Package sandbox runs untrusted programs inside locked down, short lived Docker containers.
package sandbox

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrImageMissing means the image vanished from Docker, for example after a prune
	ErrImageMissing = errors.New("image is not available in docker")
	// ErrVolumeMissing means the workspace volume was removed while the submission was running
	ErrVolumeMissing = errors.New("workspace volume is gone")
)

const (
	LabelManaged    = "code-verifier-sandbox.managed"
	LabelSubmission = "code-verifier-sandbox.submission"

	// the per submission volume is mounted here and the code lives in WorkDir
	WorkspaceMount = "/workspace"
	WorkDir        = "/workspace/box"

	// nobody on every image we use, and it does not need a passwd entry anyway
	sandboxUID = 65534
)

type File struct {
	Name string
	Data []byte
	Mode int64
}

type Spec struct {
	Image string
	Cmd   []string
	Env   []string

	// Volume is mounted at WorkspaceMount. Only the compile step gets it writable.
	Volume         string
	WritableVolume bool
	// Files are copied into WorkDir before the container starts and need a writable volume
	Files []File

	Stdin       []byte
	Timeout     time.Duration
	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
	// cap for stdout and for stderr separately, going over it kills the program
	OutputLimit int64
	TmpfsSize   string
	TmpfsExec   bool
	// largest single file the program may write, stops a compiler from filling the disk
	MaxFileBytes int64

	Labels map[string]string
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Duration time.Duration

	TimedOut            bool
	OutputLimitExceeded bool
	OOMKilled           bool
}

// Runner is what the judge needs from the sandbox. Tests swap in a fake.
type Runner interface {
	CreateVolume(ctx context.Context, labels map[string]string) (string, error)
	RemoveVolume(ctx context.Context, name string) error
	Run(ctx context.Context, spec Spec) (Result, error)
}
