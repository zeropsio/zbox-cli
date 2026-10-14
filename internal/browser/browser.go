// Package browser opens URLs in the system browser.
package browser

import (
	"os/exec"
	"runtime"
)

// Open launches the system browser without waiting for it, so a missing browser is not fatal: the caller prints the URL anyway.
func Open(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}
