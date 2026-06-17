package server

import (
	"os/exec"
	"runtime"
)

// browserOpen opens url in the user's default browser (best-effort), replacing
// Python's webbrowser.open.
func browserOpen(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
