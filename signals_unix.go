//go:build !windows

package main

import (
	"os"
	"syscall"
)

// Catch SSH hangups and service termination so a running finalizer can recover.
func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}
