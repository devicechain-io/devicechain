// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cmd

import (
	"os/exec"
	"syscall"
)

// procTree is a credential plugin and everything it starts, as one thing to kill. On Unix
// the plugin leads a new session, which is also a new process group: it has no
// controlling terminal (so it cannot open /dev/tty to prompt), and a kill of the group
// reaches every descendant that has not left it.
type procTree struct{ cmd *exec.Cmd }

func newProcTree(cmd *exec.Cmd) *procTree {
	t := &procTree{cmd: cmd}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// An Exit or the deadline kills the whole group, not only the plugin.
	cmd.Cancel = func() error { t.kill(); return nil }
	return t
}

func (t *procTree) start() error { return t.cmd.Start() }

// kill kills every process left in the plugin's group. The group outlives its leader
// while any member lives, so this reaches a child the plugin left behind.
func (t *procTree) kill() {
	if p := t.cmd.Process; p != nil {
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	}
}

func (t *procTree) close() {}
