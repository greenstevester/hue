//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// resumeSignal and pauseSignal let launchd, systemd or a shell pause and resume a running controller.
var resumeSignal, pauseSignal os.Signal = syscall.SIGUSR1, syscall.SIGUSR2
