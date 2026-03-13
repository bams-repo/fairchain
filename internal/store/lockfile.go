//go:build !windows
// +build !windows

package store

import (
	"fmt"
	"os"
	"syscall"
)

// AcquireLock creates and locks a .lock file using flock advisory locking.
// Returns the file handle (caller must defer ReleaseLock).
func AcquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another fairchain instance is using this data directory (lock file: %s)", path)
	}

	// Write PID for diagnostics.
	f.Truncate(0)
	f.Seek(0, 0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	f.Sync()

	return f, nil
}

// ReleaseLock releases the advisory lock and removes the lock file.
func ReleaseLock(f *os.File) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	name := f.Name()
	f.Close()
	os.Remove(name)
}
