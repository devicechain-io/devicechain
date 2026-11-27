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
// A command whose leader exits on its own is not chased: whatever it left running in
// its group is its own business, and the caller is done with the command. Cmd.WaitDelay
// still bounds how long Wait waits for output a leftover holds open.
package proctree
