//go:build unix

package instancelock

import (
	"errors"
	"testing"
)

func TestOnlyOneHolder(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireIn(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireIn(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire should fail with ErrAlreadyRunning, got %v", err)
	}
	release()
	again, err := AcquireIn(dir)
	if err != nil {
		t.Fatalf("lock should be free after release, got %v", err)
	}
	again()
}
