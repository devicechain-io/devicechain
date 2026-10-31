// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "syscall"

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
	errorInvalidParameter          = syscall.Errno(87) // OpenProcess on a pid that does not exist
)

// osProbeProcess opens the process for a limited query and reads its exit code. A
// pid that cannot be opened because it does not exist is not running; one refused
// for access exists. A handle to an exited process can still be opened while
// something holds it, so STILL_ACTIVE is what decides.
func osProbeProcess(pid int) procState {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		switch err {
		case errorInvalidParameter:
			return procNotRunning
		case syscall.ERROR_ACCESS_DENIED:
			return procRunning
		}
		return procUnknown
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return procUnknown
	}
	if code == stillActive {
		return procRunning
	}
	return procNotRunning
}
