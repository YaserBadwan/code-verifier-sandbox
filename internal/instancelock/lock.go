// Package instancelock makes sure only one verifier runs on a machine at a time.
// The startup and shutdown cleanup removes every sandbox container it finds, which
// is only safe when no other verifier is using them.
package instancelock

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrAlreadyRunning = errors.New("another verifier is already running on this machine, stop it first")

// Acquire takes the lock in the user cache folder. Call release on shutdown. The
// operating system drops the lock by itself if the process dies.
func Acquire() (release func(), err error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return AcquireIn(filepath.Join(dir, "code-verifier-sandbox"))
}

func AcquireIn(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "verifier.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, nil
}
