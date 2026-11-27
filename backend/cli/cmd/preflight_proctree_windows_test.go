// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"testing"

	"golang.org/x/sys/windows"
)

// procTreeReport has nothing to report on Windows: process groups are Unix's.
func procTreeReport() {}

// watchExit reports whether pid has exited, through a handle opened now, so a pid the
// system reuses later is never mistaken for it. Whatever is still running when the
// test ends is terminated.
func watchExit(t *testing.T, pid int) func() bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatalf("opening process %d: %v", pid, err)
	}
	const stillActive = 259 // STILL_ACTIVE
	gone := func() bool {
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			t.Errorf("reading process %d's exit code: %v", pid, err)
			return true
		}
		return code != stillActive
	}
	t.Cleanup(func() {
		if !gone() {
			_ = windows.TerminateProcess(h, 1)
		}
		_ = windows.CloseHandle(h)
	})
	return gone
}
