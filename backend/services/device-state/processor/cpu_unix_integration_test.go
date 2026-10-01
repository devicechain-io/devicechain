// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration && unix

package processor

import (
	"syscall"
	"time"
)

// processCPU is the CPU time this process has used so far, user and system together. ok is
// false when the platform cannot say.
func processCPU() (cpu time.Duration, ok bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), true
}
