// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proctree

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

// Run starts cmd as the leader of a process group of its own and waits for it. If ctx
// ends first, the whole group is SIGKILLed and Run returns ctx's error once the leader
// has been reaped. cmd must not have been made with exec.CommandContext: Run is what
// binds it to ctx.
//
// SIGKILL at once, with no SIGTERM grace first: the callers' commands are read-only
// queries with nothing to tidy, a grace would spend the time a caller allows a stop,
// and a group member stopped by SIGTTIN or SIGTTOU (a background group that touched the
// terminal) only takes SIGTERM once it is continued, while SIGKILL ends it where it
// stands.
//
// The group is made in the child before it execs, so nothing the command spawns is
// outside it from the start. It is not the terminal's foreground group: give the
// command a stdin that is not the terminal (nil, the null device), or a read of it
// stops the group.
//
// RunWith is Run with Options (see each).
func RunWith(ctx context.Context, cmd *exec.Cmd, o Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if o.NewSession {
		// A session leader leads its own group too; setpgid on it would fail.
		cmd.SysProcAttr.Setsid = true
	} else {
		cmd.SysProcAttr.Setpgid = true
	}
	if o.Sweep {
		if m := mark(cmd); m != "" {
			defer sweep(m)
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	// Nothing reaps the leader until cmd.Wait below, so until then pid (and the pgid it
	// names) is the command's: alive, or a zombie that holds both.
	exited := make(chan error, 1)
	go func() { exited <- waitExit(pid) }()
	select {
	case err := <-exited:
		if err != nil {
			// The exit cannot be watched without reaping: end the leader alone, through
			// exec, which is safe whenever it runs.
			return waitKillingLeader(ctx, cmd)
		}
		if o.EndTreeOnExit {
			// Unreaped, the leader still holds its pgid: the group is the command's.
			_ = signalGroup(pid)
		}
		return cmd.Wait()
	case <-ctx.Done():
		if err := signalGroup(pid); err != nil {
			_ = cmd.Process.Kill()
		}
		// The leader is SIGKILLed: its exit is seen before it is reaped, so the watcher
		// never outlives the pid it watches.
		<-exited
		_ = cmd.Wait()
		return ctx.Err()
	}
}

// signalGroup SIGKILLs the process group pgid. A variable only so a test can see when
// it is called.
var signalGroup = func(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil // the group has ended already
	}
	return err
}

// waitKillingLeader waits for cmd, killing only the leader if ctx ends first.
func waitKillingLeader(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}
