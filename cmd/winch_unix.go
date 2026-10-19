//go:build !windows

package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize delivers SIGWINCH on c, so the shell can send a resize frame when the terminal changes size.
func notifyResize(c chan<- os.Signal) {
	signal.Notify(c, syscall.SIGWINCH)
}
