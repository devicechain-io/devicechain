// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
	"golang.org/x/sys/unix"
)

// The PTY tests run the real presenter in a child process whose stdin, stdout and stderr
// are a pseudo-terminal, exactly as an operator's shell hands them over: the child probes
// the terminal itself, Bubble Tea puts it in raw mode, and the parent reads the terminal's
// modes back after the child has gone. A restore that only LOOKS right in the output
// (the escape sequences) but leaves the line discipline raw fails here.

const (
	ptyChildEnv  = "DCCTL_INSTALLTUI_PTY_CHILD"
	ptyMarkerEnv = "DCCTL_INSTALLTUI_PTY_MARKER"
)

type pty struct{ master, slave *os.File }

func openPTY(t *testing.T) pty {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening /dev/ptmx: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlocking the pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("naming the pty: %v", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("opening the pty's terminal: %v", err)
	}
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
		t.Fatalf("sizing the pty: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(); _ = s.Close() })
	return pty{master: m, slave: s}
}

func termios(t *testing.T, f *os.File) unix.Termios {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("reading the terminal's modes: %v", err)
	}
	return *tio
}

// ptyRun is one child on a fresh terminal.
type ptyRun struct {
	t      *testing.T
	pty    pty
	out    *syncBuffer
	cmd    *exec.Cmd
	marker string
	before unix.Termios
}

func startPTYChild(t *testing.T, scenario string) *ptyRun {
	t.Helper()
	r := &ptyRun{t: t, pty: openPTY(t), out: &syncBuffer{}, marker: filepath.Join(t.TempDir(), "marker")}
	r.before = termios(t, r.pty.slave)
	if r.before.Lflag&unix.ICANON == 0 || r.before.Lflag&unix.ECHO == 0 {
		t.Fatalf("a fresh pty is not in cooked mode: lflag=%#x", r.before.Lflag)
	}
	r.cmd = exec.Command(os.Args[0], "-test.run=^TestPTYChild$", "-test.count=1")
	// A built environment: in particular no CI, which every hosted runner sets and which
	// rightly keeps a session plain.
	r.cmd.Env = []string{
		ptyChildEnv + "=" + scenario, ptyMarkerEnv + "=" + r.marker,
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"TERM=xterm-256color", "LANG=C.UTF-8",
	}
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		r.cmd.Env = append(r.cmd.Env, "GOCOVERDIR="+d)
	}
	r.cmd.Stdin, r.cmd.Stdout, r.cmd.Stderr = r.pty.slave, r.pty.slave, r.pty.slave
	// As a shell starts it: in its own session, with the terminal as its controlling
	// terminal, so the kernel reports a resize to it.
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := r.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(r.out, r.pty.master) }()
	t.Cleanup(func() { _ = r.cmd.Process.Kill() })
	return r
}

func (r *ptyRun) press(s string) {
	r.t.Helper()
	if _, err := r.pty.master.Write([]byte(s)); err != nil {
		r.t.Fatal(err)
	}
}

// wait returns the child's exit status, then checks the terminal it leaves behind.
func (r *ptyRun) wait() int {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		r.t.Fatalf("the child did not exit; terminal:\n%q", r.out.String())
	}
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	after := termios(r.t, r.pty.slave)
	if after.Lflag != r.before.Lflag || after.Iflag != r.before.Iflag || after.Oflag != r.before.Oflag ||
		after.Cflag != r.before.Cflag || after.Cc != r.before.Cc {
		r.t.Errorf("the terminal was left changed:\n before %+v\n  after %+v\nterminal:\n%q", r.before, after, r.out.String())
	}
	time.Sleep(50 * time.Millisecond) // let the reader drain the last bytes
	return code
}

func (r *ptyRun) markers() string {
	b, _ := os.ReadFile(r.marker)
	return string(b)
}

func TestPTYRestoresTheTerminalOnSuccess(t *testing.T) {
	r := startPTYChild(t, "success")
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "dcctl install: succeeded")
}

func TestPTYRestoresTheTerminalOnFailure(t *testing.T) {
	r := startPTYChild(t, "failure")
	if code := r.wait(); code != 1 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "dcctl install: failed")
}

func TestPTYRestoresTheTerminalOnPanic(t *testing.T) {
	r := startPTYChild(t, "panic")
	if code := r.wait(); code != 2 {
		t.Fatalf("exit %d, want a panic's 2; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "panic: engine exploded")
}

func TestPTYCtrlCCancelsAndTheRunSettles(t *testing.T) {
	r := startPTYChild(t, "settles")
	waitFor(t, "the run", r.out, screenShows(r.out, "Resolve the target cluster"))
	r.press("\x03")
	if code := r.wait(); code != 1 {
		t.Fatalf("exit %d, want 1 for a cancelled run; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); m != "cancelled\nsettled\n" {
		t.Fatalf("engine saw %q", m)
	}
	assertRestored(t, r.out.String(), "dcctl install: cancelled")
}

func TestPTYDoubleCtrlCForcesExitWithTheTerminalRestored(t *testing.T) {
	r := startPTYChild(t, "stubborn")
	waitFor(t, "the run", r.out, screenShows(r.out, "Resolve the target cluster"))
	r.press("\x03")
	waitFor(t, "the cancel", r.out, func() bool { return r.markers() == "cancelled\n" })
	waitFor(t, "the stopping notice", r.out, screenShows(r.out, "Interrupt received."))
	r.press("\x03")
	if code := r.wait(); code != 1 {
		t.Fatalf("exit %d, want 1; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); m != "cancelled\n" {
		t.Fatalf("engine saw %q, want exactly one cancel", m)
	}
	assertRestored(t, r.out.String(), "interrupted again")
}

func TestPTYSignalThenCtrlCForcesExit(t *testing.T) {
	r := startPTYChild(t, "stubborn")
	waitFor(t, "the run", r.out, screenShows(r.out, "Resolve the target cluster"))
	if err := r.cmd.Process.Signal(unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the cancel", r.out, func() bool { return r.markers() == "cancelled\n" })
	r.press("\x03")
	if code := r.wait(); code != 1 {
		t.Fatalf("exit %d, want 1; terminal:\n%q", code, r.out.String())
	}
	assertRestored(t, r.out.String(), "interrupted again")
}

// A hangup (the terminal window closed, an SSH session dropped) is an interrupt like any
// other: it must not kill the process with the terminal still raw.
func TestPTYHangupThenCtrlCRestoresTheTerminal(t *testing.T) {
	for _, sig := range []unix.Signal{unix.SIGHUP, unix.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			r := startPTYChild(t, "stubborn")
			waitFor(t, "the run", r.out, screenShows(r.out, "Resolve the target cluster"))
			if err := r.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the cancel", r.out, func() bool { return r.markers() == "cancelled\n" })
			r.press("\x03")
			if code := r.wait(); code != 1 {
				t.Fatalf("exit %d, want 1; terminal:\n%q", code, r.out.String())
			}
			assertRestored(t, r.out.String(), "interrupted again")
		})
	}
}

// With no file descriptor to spare, Bubble Tea's input reader cannot start (it needs an
// epoll instance), and Run fails after the terminal has been put in raw mode. Before
// v2.2.0 it returned without restoring anything; the presenter restores the modes it saved
// whatever happened, the terminal comes back as it was, and the run carries on in plain
// output.
func TestPTYAnInputReaderThatCannotStartLeavesTheTerminalRestored(t *testing.T) {
	r := startPTYChild(t, "nofds")
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if !strings.Contains(r.out.String(), "continuing with plain output") {
		t.Fatalf("the program did start, so this did not test the failure path:\n%q", r.out.String())
	}
}

// 🔴 BUBBLE TEA'S SHUTDOWN READS STDIN. From v2.2.0, a program that wrote a terminal query
// in its last second sends a device-attributes request on the way out and reads the
// terminal's replies, for up to half a second, so they do not reach the shell. That read
// must end inside Run: on a terminal that never answers (this one), the presenter still
// returns, and what is typed after it returns is the process's, not a read the program
// left behind. Each run here writes a query with its first frame, so it ends in the drain.
func TestPTYNothingReadsStdinAfterThePresenterReturnsWithAQueryInFlight(t *testing.T) {
	r := startPTYChild(t, "readafter")
	waitFor(t, "the presenter's return", r.out, func() bool { return strings.Contains(r.markers(), "returned") })
	if !strings.Contains(r.out.String(), "\x1b[c") {
		t.Fatalf("the program never asked for the terminal's attributes on the way out, so its "+
			"shutdown read was not exercised:\n%q", r.out.String())
	}
	r.press("z\n")
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	if m := r.markers(); !strings.Contains(m, `read "z\n" <nil>`) {
		t.Fatalf("the process did not read what was typed after the presenter returned: %q", m)
	}
}

// The program sees a terminal on stdout, through the presenter's writer: it reads the
// terminal's size, and a resize reaches it. A writer that hid the descriptor would leave
// Bubble Tea treating stdout as a pipe: no size, no resize, and on Windows no VT mode.
func TestPTYTheProgramSeesTheTerminalAndItsResizes(t *testing.T) {
	r := startPTYChild(t, "resize")
	waitFor(t, "the first size", r.out, func() bool { return strings.Contains(r.markers(), "size 120x40") })
	if err := unix.IoctlSetWinsize(int(r.pty.slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 30, Col: 100}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the resize", r.out, func() bool { return strings.Contains(r.markers(), "size 100x30") })
	r.press("\x03")
	waitFor(t, "the cancel", r.out, func() bool { return strings.Contains(r.markers(), "cancelled") })
	r.press("\x03")
	if code := r.wait(); code != 1 {
		t.Fatalf("exit %d, want 1; terminal:\n%q", code, r.out.String())
	}
}

func TestPTYARendererFailureContinuesTheSameRunInPlainOutput(t *testing.T) {
	r := startPTYChild(t, "fallback")
	waitFor(t, "the plain question", r.out, screenShows(r.out, "Create it now? [y/N]: "))
	r.press("y\n")
	if code := r.wait(); code != 0 {
		t.Fatalf("exit %d; terminal:\n%q", code, r.out.String())
	}
	out := r.out.String()
	assertRestored(t, out, "continuing with plain output")
	if !strings.Contains(out, "after the fallback") {
		t.Fatalf("the engine's words after the fallback were lost:\n%q", out)
	}
	if m := r.markers(); m != "asked\nconfirmed\n" {
		t.Fatalf("engine saw %q; one run, one answer", m)
	}
}

// TestPTYChild is the child half. It does nothing unless a parent started it.
func TestPTYChild(t *testing.T) {
	scenario := os.Getenv(ptyChildEnv)
	if scenario == "" {
		return
	}
	mark := func(s string) {
		f, err := os.OpenFile(os.Getenv(ptyMarkerEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString(s + "\n")
			_ = f.Close()
		}
	}
	progressOnly := scenario != "stubborn" && scenario != "fallback" && scenario != "resize"
	in := Probe(os.Stdin, os.Stdout, os.Stderr)
	in.UI, in.Yes, in.NoSplash = terminal.TUI, progressOnly, true
	caps := terminal.Evaluate(in)
	if !caps.TUI || caps.ProgressOnly != progressOnly {
		fmt.Fprintf(os.Stderr, "the pty was not eligible: %+v\n", caps)
		os.Exit(3)
	}
	engine := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		switch scenario {
		case "success", "nofds", "readafter":
			return succeeded, endRun(rt)
		case "failure":
			return installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
				Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
				PartialState: installflow.EffectMayHaveChanged}, errors.New("the operator rollout timed out")
		case "panic":
			panic("engine exploded")
		case "fallback":
			mark("asked")
			ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
				Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
				Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}})
			if err != nil || !ans.Confirmed {
				return cancelled, fmt.Errorf("not confirmed: %v", err)
			}
			mark("confirmed")
			fmt.Fprintln(rt.Console.Stdout, "after the fallback")
			return succeeded, endRun(rt)
		}
		<-ctx.Done()
		mark("cancelled")
		if scenario == "stubborn" || scenario == "resize" {
			select {} // still stopping gracefully
		}
		mark("settled")
		return cancelled, ctx.Err()
	})
	p := Presenter{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps}
	if scenario == "resize" {
		p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if ws, ok := msg.(tea.WindowSizeMsg); ok {
				mark(fmt.Sprintf("size %dx%d", ws.Width, ws.Height))
			}
			return msg
		})}
	}
	if scenario == "fallback" {
		// The renderer dies when the question reaches it, after the run has begun.
		p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(promptMsg); ok {
				panic("renderer failed")
			}
			return msg
		})}
	}
	if scenario == "nofds" {
		// Leave no descriptor free, so the program's input reader cannot create its
		// epoll instance and the program is not run. The runtime's own poller is started first
		// (a deadline on a pipe needs it), so only the program's reader goes short.
		pr, pw, err := os.Pipe()
		if err != nil || pr.SetReadDeadline(time.Now()) != nil {
			os.Exit(4)
		}
		defer pr.Close()
		defer pw.Close()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			os.Exit(4)
		}
		lim := uint64(len(entries) - 1) // ReadDir's own descriptor is closed again
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: lim, Max: lim}); err != nil {
			os.Exit(4)
		}
	}
	err := p.Run(context.Background(),
		engine, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: progressOnly}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
	if scenario == "readafter" {
		// The process reads the terminal itself once the presenter has returned, as the
		// shell does once dcctl exits.
		mark("returned")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		mark(fmt.Sprintf("read %q %v", line, err))
	}
	os.Exit(0)
}
