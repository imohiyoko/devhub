//go:build windows

package ports

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// killProcess terminates the process via taskkill /F (Windows has no SIGTERM).
func killProcess(pid int) error {
	out, err := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").CombinedOutput() //execaudit:ports-kill
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("taskkill failed")
	}
	return err
}
