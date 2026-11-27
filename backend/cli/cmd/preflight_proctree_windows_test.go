// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procTreeReport is the console role on Windows (process groups are Unix's):
// "proctree: <pid> <whether it shares its parent's console>".
func procTreeReport() {
	fmt.Printf("%s %d %t\n", procTreeLine, os.Getpid(), slices.Contains(consoleProcesses(), uint32(os.Getppid())))
}

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// consoleProcesses lists the processes attached to this process's console (none when it
// has no console).
func consoleProcesses() []uint32 {
	ids := make([]uint32, 256)
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	if n == 0 || int(n) > len(ids) {
		return nil
	}
	return ids[:n]
}

// 🔴 A SCREEN COMMAND NEVER SHARES dcctl's CONSOLE. kubectl's exec credential plugin
// asking for an MFA code reads the console directly, whatever its stdin is: on dcctl's
// console it would take the keystrokes meant for the screen. The command gets no
// console of dcctl's.
func TestAScreenCommandDoesNotShareTheConsole(t *testing.T) {
	if !slices.Contains(consoleProcesses(), uint32(os.Getpid())) {
		t.Skip("this test process has no console to share")
	}
	t.Setenv(procTreeRoleEnv, "console")
	f := parseProcTreeReport(t, runContext(context.Background(), os.Args[0], procTreeArgs...))
	if len(f) != 2 {
		t.Fatalf("the helper reported %q", f)
	}
	if f[1] != "false" {
		t.Fatal("the screen's command shares dcctl's console: a credential plugin could read the screen's keystrokes")
	}
}

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
