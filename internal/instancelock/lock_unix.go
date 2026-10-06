//go:build unix

package instancelock

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrAlreadyRunning
	}
	return err
}

func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
