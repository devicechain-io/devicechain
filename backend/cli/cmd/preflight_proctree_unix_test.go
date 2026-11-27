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

	"golang.org/x/sys/unix"
)

// procTreeReport is the pgid role: "proctree: <pid> <pgid> <stdin is the null device>
// <session id> <whether /dev/tty opens>".
func procTreeReport() {
	null := false
	if in, err := os.Stdin.Stat(); err == nil {
		if dev, err := os.Stat(os.DevNull); err == nil {
			null = os.SameFile(in, dev)
		}
	}
	sid, _ := unix.Getsid(0)
	tty := false
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		tty = true
		_ = f.Close()
	}
	fmt.Printf("%s %d %d %t %d %t\n", procTreeLine, os.Getpid(), syscall.Getpgrp(), null, sid, tty)
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

// pgidReport is what the pgid role reported of itself.
type pgidReport struct {
	pid, pgid, sid int
	nullStdin, tty bool
}

func runPgidHelper(t *testing.T, run func() string) pgidReport {
	t.Helper()
	t.Setenv(procTreeRoleEnv, "pgid")
	f := parseProcTreeReport(t, run())
	if len(f) != 5 {
		t.Fatalf("the helper reported %q", f)
	}
	pid, err1 := strconv.Atoi(f[0])
	pgid, err2 := strconv.Atoi(f[1])
	sid, err3 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("the helper reported %q", f)
	}
	return pgidReport{pid: pid, pgid: pgid, sid: sid, nullStdin: f[2] == "true", tty: f[4] == "true"}
}

// A screen command leads a process group of its own, which is what lets its whole tree
// be ended; and it reads the null device, so being outside the terminal's foreground
// group takes nothing from it (a background group that read the terminal would be
// stopped).
func TestAScreenCommandLeadsAProcessGroupOfItsOwn(t *testing.T) {
	r := runPgidHelper(t, func() string {
		return runContext(context.Background(), os.Args[0], procTreeArgs...)
	})
	if r.pgid != r.pid || r.pgid == syscall.Getpgrp() {
		t.Fatalf("the command (pid %d) ran in process group %d; dcctl's is %d", r.pid, r.pgid, syscall.Getpgrp())
	}
	if !r.nullStdin {
		t.Fatal("the command's stdin is not the null device: it could read the terminal the screen owns")
	}
}

// 🔴 A SCREEN COMMAND CAN NEVER PROMPT. kubectl's exec credential plugin asking for an
// MFA code opens /dev/tty itself; in a background group of dcctl's session it would be
// stopped there by SIGTTOU, and the check would wait on it for ever. The command leads a
// session of its own, so it has no controlling terminal and the open fails at once.
func TestAScreenCommandHasNoControllingTerminal(t *testing.T) {
	r := runPgidHelper(t, func() string {
		return runContext(context.Background(), os.Args[0], procTreeArgs...)
	})
	own, _ := unix.Getsid(0)
	if r.sid != r.pid || r.sid == own {
		t.Fatalf("the command (pid %d) ran in session %d; dcctl's is %d", r.pid, r.sid, own)
	}
	if r.tty {
		t.Fatal("the command could open /dev/tty: a credential plugin could prompt on the screen's terminal")
	}
}

// 🔴 THE PLAIN DOCTOR IS UNCHANGED: `dcctl preflight`, and every install's own preflight
// stage, run their commands in dcctl's own process group, as they always have. A Ctrl+C
// on a cooked terminal signals the foreground group, so a command moved out of it would
// no longer hear the operator's interrupt.
func TestThePlainDoctorsCommandsStayInDcctlsProcessGroup(t *testing.T) {
	r := runPgidHelper(t, func() string { return run(os.Args[0], procTreeArgs...) })
	if r.pgid != syscall.Getpgrp() {
		t.Fatalf("the plain command (pid %d) ran in process group %d, not dcctl's %d", r.pid, r.pgid, syscall.Getpgrp())
	}
}
