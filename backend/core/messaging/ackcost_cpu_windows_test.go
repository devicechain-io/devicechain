// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"syscall"
	"time"
)

// ackProcessCPU is the CPU time this process has used so far, user and system together.
func ackProcessCPU() time.Duration {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		panic(err)
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		panic(err)
	}
	// Filetime counts 100 ns ticks.
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration((ticks(kernel) + ticks(user)) * 100)
}
