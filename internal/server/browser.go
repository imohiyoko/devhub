package server

import (
	"os/exec"
	"runtime"
)

// browserOpen opens url in the user's default browser (best-effort).
func browserOpen(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url) //execaudit:browser-open
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url) //execaudit:browser-open
	default:
		cmd = exec.Command("xdg-open", url) //execaudit:browser-open
	}
	_ = cmd.Start()
}
