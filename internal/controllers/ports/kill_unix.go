//go:build !windows

package ports

import (
	"errors"
	"fmt"
	"syscall"
)

// killProcess sends SIGTERM, mapping permission/lookup errors to user-facing messages.
func killProcess(pid int) error {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, syscall.EPERM):
		return fmt.Errorf("permission denied: cannot kill this process")
	case errors.Is(err, syscall.ESRCH):
		return fmt.Errorf("process was not found or has already exited")
	default:
		return err
	}
}
