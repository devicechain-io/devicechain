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
	for _, args := range [][]string{
		{"install", "local", "--ui=plain", "--help"},
		{"install", "--help"},
		{"--help"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			m, s := openCmdPTY(t)
			before, err := unix.IoctlGetTermios(int(s.Fd()), unix.TCGETS)
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
			go func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					if now, err := unix.IoctlGetTermios(int(s.Fd()), unix.TCGETS); err == nil && now.Lflag != before.Lflag {
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
			if took > time.Second {
				t.Errorf("dcctl %v took %v on a terminal that answers no queries", args, took)
			}
		})
	}
}

const plainPTYChildEnv = "DCCTL_PLAIN_PTY_CHILD"

// TestPlainPTYChild is the child half: dcctl with the arguments the parent passed.
func TestPlainPTYChild(t *testing.T) {
	n := os.Getenv(plainPTYChildEnv)
	if n == "" {
		return
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
