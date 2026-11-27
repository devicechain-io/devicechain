// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitExit returns once the child pid has exited, without reaping it: waitid with
// WNOWAIT leaves the zombie, and with it the pid and its process group, to Cmd.Wait.
func waitExit(pid int) error {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
