//go:build !unix

package instancelock

import "os"

// no flock outside unix, so there is no protection against two instances here
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) {}
