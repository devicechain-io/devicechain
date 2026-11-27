// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cmd

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// startTree starts cmd as the leader of a process group of its own, and makes the end
// of its context (exec.Cmd.Cancel) SIGKILL the whole group: the command and everything
// it spawned that has not left the group, such as an exec credential plugin kubectl
// started. The group is made in the child before it execs, so nothing the command
// spawns can be outside it from the start.
//
// SIGKILL at once, with no SIGTERM grace first. The commands are the doctor's
// read-only queries (docker info, kubectl version): nothing in them needs a chance to
// tidy up, and a stop that waited would spend the commandWaitDelay the screen allows.
// A grace would also not reach everything it was meant for: a member of a background
// group that touches the terminal is stopped (SIGTTIN, SIGTTOU), and a stopped process
// only takes SIGTERM once it is continued, while SIGKILL ends it where it stands. The
// command alone was already SIGKILLed before this (os.Process.Kill); the group gets
// what its leader got.
//
// The group is not the terminal's foreground group, so a keypress on the terminal
// (were it in cooked mode) would not signal it, and it cannot read the terminal: its
// stdin is the null device anyway (runContext). An interrupt reaches dcctl, which
// cancels the screen's context, which ends the groups through Cancel.
func startTree(cmd *exec.Cmd) (end func(), err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	return func() {}, cmd.Start()
}

// killGroup SIGKILLs the process group led by pgid. A group already gone is the
// process done, which Cancel's caller treats as nothing to report.
func killGroup(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
