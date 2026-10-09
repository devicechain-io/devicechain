// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package messaging

import (
	"syscall"
	"time"
)

// ackProcessCPU is the CPU time this process has used so far, user and system together.
func ackProcessCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		panic(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
