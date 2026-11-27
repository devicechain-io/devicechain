// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPlainDcctlNeverTouchesTheTerminal runs dcctl as an operator's shell does: in its own
// session, with a pseudo-terminal as its controlling terminal and every standard stream,
// in the foreground. Nothing that answers terminal queries is attached, as with a terminal
// that does not implement them.
//
// 🔴 PLAIN OUTPUT MUST NOT QUERY OR RECONFIGURE THE TERMINAL, AND NOTHING IN THE BINARY'S
// INITIALISATION MAY EITHER. A package initializer runs for every command dcctl has, whatever
// its flags, so a library that asks the terminal for its background colour at init (an OSC
// 11 query and a cursor report, with echo and canonical input switched off while it waits)
// does it to `dcctl version` as much as to `dcctl install --ui=plain` — and, on a terminal
// that does not answer, waits out its timeout first.
func TestPlainDcctlNeverTouchesTheTerminal(t *testing.T) {
	if os.Getenv(plainPTYChildEnv) != "" {
		return
	}
	// The child is this test binary, whose start-up alone can take most of a second under
	// -race on a CI runner. Time one child that exits before dcctl runs, and hold each
	// dcctl run to that plus a margin far below any terminal-query timeout.
	baseline := timePTYChild(t)
	for _, args := range [][]string{
		{"install", "local", "--ui=plain", "--help"},
		{"install", "--help"},
		{"--help"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			m, s := openCmdPTY(t)
			sfd := int(s.Fd())
			before, err := unix.IoctlGetTermios(sfd, unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestPlainPTYChild$", "-test.count=1")
			cmd.Env = []string{plainPTYChildEnv + "=" + fmt.Sprint(len(args)), "PATH=" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(), "TERM=xterm-256color", "LANG=C.UTF-8"}
			for i, a := range args {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s_%d=%s", plainPTYChildEnv, i, a))
			}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			var out bytes.Buffer
			var mu sync.Mutex
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := m.Read(buf)
					mu.Lock()
					out.Write(buf[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			// Watch the line discipline for the whole run: a mode switched off and back on
			// leaves nothing to see afterwards.
			changed := make(chan string, 1)
			stop := make(chan struct{})
			watched := make(chan struct{})
			go func() {
				defer close(watched)
				for {
					select {
					case <-stop:
						return
					default:
					}
					if now, err := unix.IoctlGetTermios(sfd, unix.TCGETS); err == nil && now.Lflag != before.Lflag {
						select {
						case changed <- fmt.Sprintf("lflag %#x -> %#x", before.Lflag, now.Lflag):
						default:
						}
					}
					time.Sleep(time.Millisecond)
				}
			}()
			began := time.Now()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			took := time.Since(began)
			close(stop)
			// The watcher must be gone before the cleanup closes the terminal it polls.
			<-watched
			var ee *exec.ExitError
			if err != nil && !errors.As(err, &ee) {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			got := out.String()
			mu.Unlock()
			for _, q := range []string{"\x1b]", "\x1b[6n", "\x1b[?"} {
				if bytes.Contains([]byte(got), []byte(q)) {
					t.Errorf("dcctl %v wrote the terminal control sequence %q:\n%q", args, q, got)
				}
			}
			select {
			case c := <-changed:
				t.Errorf("dcctl %v changed the terminal's modes while it ran: %s", args, c)
			default:
			}
			if limit := baseline + 750*time.Millisecond; took > limit {
				t.Errorf("dcctl %v took %v on a terminal that answers no queries (a child that runs nothing took %v)", args, took, baseline)
			}
		})
	}
}

const plainPTYChildEnv = "DCCTL_PLAIN_PTY_CHILD"

// timePTYChild runs the child on a fresh terminal with nothing for dcctl to do and
// returns how long the process took from start to exit.
func timePTYChild(t *testing.T) time.Duration {
	t.Helper()
	m, s := openCmdPTY(t)
	go func() { _, _ = io.Copy(io.Discard, m) }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlainPTYChild$", "-test.count=1")
	cmd.Env = []string{plainPTYChildEnv + "=noop", "PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"TERM=xterm-256color", "LANG=C.UTF-8"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	began := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatalf("baseline child: %v", err)
	}
	return time.Since(began)
}

// TestPlainPTYChild is the child half: dcctl with the arguments the parent passed.
func TestPlainPTYChild(t *testing.T) {
	n := os.Getenv(plainPTYChildEnv)
	if n == "" {
		return
	}
	if n == "noop" {
		os.Exit(0)
	}
	var args []string
	for i := 0; ; i++ {
		a, ok := os.LookupEnv(fmt.Sprintf("%s_%d", plainPTYChildEnv, i))
		if !ok {
			break
		}
		args = append(args, a)
	}
	rootCmd.SetArgs(args)
	Execute()
	os.Exit(0)
}

func openCmdPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(); _ = s.Close() })
	var _ io.Reader = m
	return m, s
}

// 🔴 A SIGNAL TO DCCTL WHILE THE PREFLIGHT SCREEN'S CHECKS RUN LEAVES NONE OF THEIR
// PROCESSES BEHIND. The terminal is raw, so this is not Ctrl+C (a keypress): it is an
// external SIGTERM, to dcctl alone, as `kill` or a supervisor sends it. dcctl cancels the
// screen, and the cancel ends each running check's whole process tree: here a docker that
// spawned a grandchild holding its output (a credential helper) and then stalled. Neither
// gets the signal itself, so only dcctl can end them.
func TestPTYSigtermDuringThePreflightScreenEndsItsCommandsTrees(t *testing.T) {
	if os.Getenv(plainPTYChildEnv) != "" {
		return
	}
	bin := t.TempDir()
	pids := filepath.Join(t.TempDir(), "pids")
	// Every tool the doctor looks for is this script. `docker info` is the screen's first
	// command for a local install; it spawns a sleeper holding its output, records both
	// pids, and stalls. Anything else fails at once, as an absent daemon would.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = info ]; then\n" +
		"  /bin/sleep 120 &\n" +
		"  echo \"$$ $!\" >> \"$DCCTL_PTY_TOOL_PIDS\"\n" +
		"  wait\n" +
		"fi\n" +
		"exit 1\n"
	for _, tool := range []string{"docker", "kubectl", "kind", "tofu", "terraform", "ko", "cloud-provider-kind"} {
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, s := openCmdPTY(t)
	var out bytes.Buffer
	var mu sync.Mutex
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			mu.Lock()
			out.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	screen := func() string { mu.Lock(); defer mu.Unlock(); return out.String() }
	args := []string{"install", "local", "--ui=tui", "--no-splash", "--version", "v1.0.0"}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPlainPTYChild$", "-test.count=1")
	cmd.Env = []string{plainPTYChildEnv + "=" + fmt.Sprint(len(args)), "PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + t.TempDir(), "TERM=xterm-256color", "LANG=C.UTF-8", "DCCTL_PTY_TOOL_PIDS=" + pids}
	for i, a := range args {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s_%d=%s", plainPTYChildEnv, i, a))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s, s, s
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	// until waits for ok. Before the signal, dcctl exiting is a failure of its own.
	until := func(what string, d time.Duration, ok func() bool, dcctlMayExit bool) {
		t.Helper()
		for deadline := time.Now().Add(d); !ok(); time.Sleep(20 * time.Millisecond) {
			select {
			case <-exited:
				if !dcctlMayExit {
					t.Fatalf("dcctl exited (%v) while waiting for %s; terminal:\n%q", waitErr, what, screen())
				}
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("waited %v for %s; terminal:\n%q", d, what, screen())
			}
		}
	}

	// The welcome screen; Enter goes on to the preflight screen, whose checks start.
	until("the welcome screen", 30*time.Second, func() bool { return strings.Contains(screen(), "Enter") }, false)
	if _, err := m.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	var recorded []byte
	until("the screen's checks to run docker info", 30*time.Second, func() bool {
		recorded, _ = os.ReadFile(pids)
		return len(recorded) > 0 && recorded[len(recorded)-1] == '\n'
	}, false)
	var tree []int
	for _, f := range strings.Fields(string(recorded)) {
		var pid int
		if _, err := fmt.Sscan(f, &pid); err != nil {
			t.Fatalf("the tool recorded %q", recorded)
		}
		tree = append(tree, pid)
	}
	gone := make([]func() bool, len(tree))
	for i, pid := range tree {
		gone[i] = watchExit(t, pid)
		if gone[i]() {
			t.Fatalf("process %d of the check ended before the signal", pid)
		}
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	signalled := time.Now()
	t.Logf("signalled dcctl with the check tree %v running", tree)
	for i, pid := range tree {
		until(fmt.Sprintf("process %d of the stalled check to end", pid), commandWaitDelay-time.Since(signalled), gone[i], true)
	}
	// dcctl itself leaves with nothing confirmed; a second interrupt is never needed for it.
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("dcctl did not exit after the signal; terminal:\n%q", screen())
	}
}
