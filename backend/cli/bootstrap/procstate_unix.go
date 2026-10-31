// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package bootstrap

import (
	"errors"
	"syscall"
)

// osProbeProcess asks with signal 0, which performs the existence and permission
// checks and delivers nothing. os.FindProcess alone is not enough on Unix: it
// succeeds for any pid without looking.
func osProbeProcess(pid int) procState {
	switch err := syscall.Kill(pid, 0); {
	case err == nil:
		return procRunning
	case errors.Is(err, syscall.ESRCH):
		return procNotRunning
	case errors.Is(err, syscall.EPERM):
		// Exists, owned by someone we may not signal.
		return procRunning
	default:
		return procUnknown
	}
}
