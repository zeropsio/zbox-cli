//go:build windows

package cmd

import "os"

// notifyResize does nothing on Windows, which has no SIGWINCH: the shell keeps the size it opened with.
func notifyResize(chan<- os.Signal) {}
