// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proctree

import (
	"bytes"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// sweep ends every process (but this one) whose environment carries the mark m
// (Options.Sweep). Each is pinned with a pidfd and its mark read again through the pin
// before it is signalled, so a pid reused between the read and the signal is never
// signalled. Without pidfds (a kernel before 5.3) nothing is signalled.
//
// No process-wide state is changed: dcctl does not become a subreaper, so what is ended
// here is reaped by whoever reaps it now, and no other command's orphans become dcctl's.
func sweep(m string) {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || !marked(pid, m) {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		if marked(pid, m) {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		}
		_ = unix.Close(fd)
	}
}

// marked reports whether process pid's environment holds the entry m exactly.
func marked(pid int, m string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return false
	}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if string(kv) == m {
			return true
		}
	}
	return false
}
