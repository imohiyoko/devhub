package storage

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockAgentTokenFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Chmod(0o600)
	overlap := new(windows.Overlapped)
	handle := windows.Handle(f.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlap); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlap)
		_ = f.Close()
	}, nil
}
