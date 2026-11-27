// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package proctree runs a command whose whole process tree ends with a context: the
// command and whatever it spawned (an exec credential plugin kubectl started, a
// credential helper docker started), not only the command.
//
// 🔴 A TREE IS ENDED ONLY THROUGH AN IDENTITY THAT CANNOT HAVE BEEN REUSED. On Unix the
// tree is the command's process group, named by the leader's pid; that pgid stays the
// command's only while the leader is unreaped (alive, or a zombie), so the group is
// signalled before the leader is reaped, never after. exec.Cmd.Cancel cannot promise
// that (Cmd.Wait reaps the leader before it joins the goroutine that calls Cancel), so
// Run watches for the exit itself without reaping (waitid WNOWAIT on Linux, a kqueue
// NOTE_EXIT on macOS) and calls Cmd.Wait, which reaps, only after the group has been
// signalled or the leader has exited on its own. On Windows the tree is a job object
// held by handle, which no other process can come to own.
//
// A command whose leader exits on its own is not chased by Run: whatever it left running
// in its group is its own business, and the caller is done with the command. A caller
// that does not trust the command asks for it (Options.EndTreeOnExit). Cmd.WaitDelay
// still bounds how long Wait waits for output a leftover holds open.
package proctree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
)

// Run is RunWith with no options: what the doctor's commands get.
func Run(ctx context.Context, cmd *exec.Cmd) error { return RunWith(ctx, cmd, Options{}) }

// Options are what RunWith does beyond Run, for a command that is not trusted to behave:
// the advisory checks' exec credential plugins. The zero value is Run.
type Options struct {
	// NewSession runs the command with no controlling terminal and no console of dcctl's:
	// on Unix it leads a new session (and so a new process group) instead of a new
	// process group in dcctl's session, so it cannot open /dev/tty to prompt; on Windows
	// it gets no console window (CREATE_NO_WINDOW) and a process group of its own, so it
	// cannot read dcctl's console and a Ctrl+C there is not delivered to it.
	NewSession bool
	// EndTreeOnExit ends whatever the command left in its tree when it exits on its own
	// too, not only when ctx ends. On Unix the group is signalled once the exit is seen
	// and before the leader is reaped, so the pgid is still the command's. On Windows the
	// job is closed when Run returns, which always ends what is left in it.
	EndTreeOnExit bool
	// Sweep marks the command's environment with a value of this run's own and, when Run
	// returns, ends every process still carrying it: a descendant that left the process
	// group (a setsid of its own) and so escaped the group kill. Linux only (it reads
	// /proc/<pid>/environ, and signals through a pidfd after checking the mark again, so
	// a reused pid is never signalled); elsewhere it does nothing. A descendant that both
	// leaves the group and drops its environment is out of its reach.
	Sweep bool
}

// sweepEnv is the variable Options.Sweep marks a command's environment with.
const sweepEnv = "DCCTL_PROCTREE_RUN"

// mark gives cmd this run's sweep mark and returns it ("KEY=value"), or "" when the
// mark cannot be made, in which case nothing is swept.
func mark(cmd *exec.Cmd) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	m := sweepEnv + "=" + hex.EncodeToString(b)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, m)
	return m
}
