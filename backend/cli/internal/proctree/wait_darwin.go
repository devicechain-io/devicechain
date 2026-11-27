// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitExit returns once the child pid has exited, without reaping it: a kqueue
// NOTE_EXIT reports the exit and leaves the zombie, and with it the pid and its process
// group, to Cmd.Wait. A pid that has already exited cannot be registered (ESRCH); as it
// is unreaped, that is the same answer.
func waitExit(pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(kq) }()
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	events := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, []unix.Kevent_t{change}, events, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ESRCH):
			return nil
		case err != nil:
			return err
		case n == 1 && events[0].Flags&unix.EV_ERROR != 0:
			if unix.Errno(events[0].Data) == unix.ESRCH {
				return nil
			}
			return unix.Errno(events[0].Data)
		case n == 1:
			return nil
		}
	}
}
