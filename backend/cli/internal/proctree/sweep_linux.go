// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"context"
	"io"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// procRoot is the procfs the sweep reads; a variable only so a test can present many
// fake processes.
var procRoot = "/proc"

// environCap is the most of one process's environment a sweep reads. The mark is a short
// entry, so a process whose environment is larger is read no further.
const environCap = 64 << 10

// sweepSlot lets one sweep run at a time: a Recheck's sweep waits for the one before it,
// and gives up, incomplete, if its deadline passes while it waits.
var sweepSlot = make(chan struct{}, 1)

// sweep ends every process (but this one) whose environment carries the mark m
// (Options.Sweep), and reports whether it looked at every process. Each is pinned with a
// pidfd and its mark read again through the pin before it is signalled, so a pid reused
// between the read and the signal is never signalled. Without pidfds (a kernel before
// 5.3) nothing is signalled.
//
// 🔴 IT IS BOUNDED: it stops at ctx's deadline, reads at most environCap of an
// environment, skips a process it cannot read at the first error, and never runs beside
// another sweep. Stopping early is reported (false), never taken for a clean sweep.
//
// No process-wide state is changed: dcctl does not become a subreaper, so what is ended
// here is reaped by whoever reaps it now, and no other command's orphans become dcctl's.
func sweep(ctx context.Context, m string) bool {
	select {
	case sweepSlot <- struct{}{}:
		defer func() { <-sweepSlot }()
	case <-ctx.Done():
		return false
	}
	dir, err := os.Open(procRoot)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	self := os.Getpid()
	for {
		names, err := dir.Readdirnames(256)
		for _, name := range names {
			if ctx.Err() != nil {
				return false
			}
			pid, perr := strconv.Atoi(name)
			if perr != nil || pid == self || !marked(pid, m) {
				continue
			}
			fd, ferr := unix.PidfdOpen(pid, 0)
			if ferr != nil {
				continue
			}
			if marked(pid, m) {
				_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
			}
			_ = unix.Close(fd)
		}
		switch {
		case err == io.EOF:
			return true // every process was looked at
		case err != nil:
			return false
		}
	}
}

// marked reports whether process pid's environment holds the entry m exactly, within its
// first environCap bytes.
func marked(pid int, m string) bool {
	f, err := os.Open(procRoot + "/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	b, _ := io.ReadAll(io.LimitReader(f, environCap))
	for start := 0; start < len(b); {
		end := start
		for end < len(b) && b[end] != 0 {
			end++
		}
		// An entry cut off by the cap is not whole, and is not taken for the mark.
		if string(b[start:end]) == m && (end < len(b) || len(b) < environCap) {
			return true
		}
		start = end + 1
	}
	return false
}
