//go:build windows
// +build windows

package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// AcquireLock creates and locks a .lock file using Windows file locking.
// Returns the file handle (caller must defer ReleaseLock).
func AcquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create lock file: %w", err)
	}

	h := windows.Handle(f.Fd())
	var ol windows.Overlapped
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another fairchain instance is using this data directory (lock file: %s)", path)
	}

	// Write PID for diagnostics.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	return f, nil
}

// ReleaseLock releases the lock and removes the lock file.
func ReleaseLock(f *os.File) {
	if f == nil {
		return
	}
	h := windows.Handle(f.Fd())
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(h, 0, 1, 0, &ol)
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}
