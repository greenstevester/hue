//go:build windows

package cmd

import "os"

// Windows has no user signals. Pause and resume through the web page or the agent tools instead.
var resumeSignal, pauseSignal os.Signal
