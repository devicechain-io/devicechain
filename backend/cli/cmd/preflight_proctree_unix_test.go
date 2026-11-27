// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// procTreeReport is the pgid role: "proctree: <pid> <pgid> <stdin is the null device>".
func procTreeReport() {
	null := false
	if in, err := os.Stdin.Stat(); err == nil {
		if dev, err := os.Stat(os.DevNull); err == nil {
			null = os.SameFile(in, dev)
		}
	}
	fmt.Printf("%s %d %d %t\n", procTreeLine, os.Getpid(), syscall.Getpgrp(), null)
}

// watchExit reports whether pid has exited. A zombie has: it runs nothing, and only
// its parent (here the init process, or whatever reaps orphans in a container) has not
// collected it yet. Whatever is still running when the test ends is killed.
func watchExit(t *testing.T, pid int) func() bool {
	t.Helper()
	gone := func() bool {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		switch {
		case err == nil:
			s := string(b)
			if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
				return s[i+2] == 'Z' || s[i+2] == 'X'
			}
			return false
		case runtime.GOOS == "linux" && errors.Is(err, fs.ErrNotExist):
			return true
		}
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}
	t.Cleanup(func() {
		if !gone() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return gone
}

func runPgidHelper(t *testing.T, run func() string) (pid, pgid int, nullStdin bool) {
	t.Helper()
	t.Setenv(procTreeRoleEnv, "pgid")
	f := parseProcTreeReport(t, run())
	if len(f) != 3 {
		t.Fatalf("the helper reported %q", f)
	}
	pid, err1 := strconv.Atoi(f[0])
	pgid, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("the helper reported %q", f)
	}
	return pid, pgid, f[2] == "true"
}

// A screen command leads a process group of its own, which is what lets its whole tree
// be ended; and it reads the null device, so being outside the terminal's foreground
// group takes nothing from it (a background group that read the terminal would be
// stopped).
func TestAScreenCommandLeadsAProcessGroupOfItsOwn(t *testing.T) {
	pid, pgid, null := runPgidHelper(t, func() string {
		return runContext(context.Background(), os.Args[0], procTreeArgs...)
	})
	if pgid != pid || pgid == syscall.Getpgrp() {
		t.Fatalf("the command (pid %d) ran in process group %d; dcctl's is %d", pid, pgid, syscall.Getpgrp())
	}
	if !null {
		t.Fatal("the command's stdin is not the null device: it could read the terminal the screen owns")
	}
}

// 🔴 THE PLAIN DOCTOR IS UNCHANGED: `dcctl preflight`, and every install's own preflight
// stage, run their commands in dcctl's own process group, as they always have. A Ctrl+C
// on a cooked terminal signals the foreground group, so a command moved out of it would
// no longer hear the operator's interrupt.
func TestThePlainDoctorsCommandsStayInDcctlsProcessGroup(t *testing.T) {
	pid, pgid, _ := runPgidHelper(t, func() string { return run(os.Args[0], procTreeArgs...) })
	if pgid != syscall.Getpgrp() {
		t.Fatalf("the plain command (pid %d) ran in process group %d, not dcctl's %d", pid, pgid, syscall.Getpgrp())
	}
}
