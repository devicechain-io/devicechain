// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Presenter runs an engine under the terminal UI.
//
// It is chosen only for a session terminal.Evaluate found eligible (all three streams a
// terminal, not CI, not dumb), so it may take the terminal over; everything else stays on
// the plain presenter, byte for byte.
type Presenter struct {
	// Stdin is the terminal the program reads keys from. It is the ONE reader of stdin
	// while the program runs: questions are answered on screen, never by a second read.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Caps   terminal.Capabilities

	// Test seams. Zero values are the real process: signal.Notify for SIGINT and
	// SIGTERM, os.Exit, and no extra program options.
	// OwnProcessStreams lets the presenter take the process's own os.Stdin, os.Stdout and
	// os.Stderr for as long as the screen is up. The command sets it; tests set it.
	OwnProcessStreams bool
	// onFinish, a test seam, is called with the hub once the run has finished.
	onFinish func(*installflow.Hub)
	// consoleBypassesGate is true when the program reads the console itself rather than
	// through the stdin gate: a Windows console. Run sets it from the platform; tests set it.
	consoleBypassesGate bool
	notify              func(chan<- os.Signal) (stop func())
	exit                func(code int)
	options             []tea.ProgramOption
}

// errIncomplete matches the plain presenter: no error from the engine, yet no success.
var errIncomplete = errors.New("the run's progress could not be reported, so it is not known to have finished")

// errForced is returned only when the exit seam returns, as it does in tests.
var errForced = errors.New("interrupted twice: exited without waiting for the operation to stop")

type engineResult struct {
	sum      installflow.Summary
	err      error
	panicked any
	stack    []byte
}

// Run executes e on screen and returns the engine's own error unchanged, so the command
// prints, and exits with, what it always has.
//
// 🔴 THE TERMINAL IS RESTORED BEFORE ANYTHING ELSE IS PRINTED, ON EVERY PATH. Bubble Tea's
// Run restores raw mode, the cursor and the alternate screen before it returns, on a
// quit, a kill or a panic it recovers; this method prints nothing, panics with nothing
// and exits nothing until that return. A panic in the ENGINE happens on another
// goroutine, where Bubble Tea cannot see it: it is recovered there, the program is
// killed, and the panic is raised again here, after the restore.
func (p Presenter) Run(ctx context.Context, e installflow.Engine, req installflow.Request,
	preflight installflow.PreflightRunner) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var prog *tea.Program
	b := &bridge{
		send:      func(m tea.Msg) { prog.Send(m) },
		done:      make(chan struct{}),
		plainSink: installplain.Sink{W: p.Stdout},
		plainErr:  p.Stderr,
	}
	hub, err := installflow.NewHub(installflow.Options{
		Operation: req.Operation, Sink: b,
		// A screen that cannot keep up stops the run gracefully, as the plain one does.
		OnDeliveryFailure: cancel,
	})
	if err != nil {
		return err
	}
	logOut, err := hub.LogWriter("", "dcctl", installflow.LogInfo)
	if err != nil {
		return err
	}
	logErr, err := hub.LogWriter("", "dcctl", installflow.LogWarn)
	if err != nil {
		return err
	}
	// Taken before anything starts, so a failure here leaves nothing running.
	var streamsDropped bool
	if p.OwnProcessStreams {
		streams, err := takeProcessStreams(hub)
		if err != nil {
			return err
		}
		e = streamsEngine{Engine: e, streams: streams, dropped: &streamsDropped}
	}
	con := installflow.Console{
		Stdout: &switchWriter{b: b, log: logOut, plain: p.Stdout, flush: hub.Flush},
		Stderr: &switchWriter{b: b, log: logErr, plain: p.Stderr, flush: hub.Flush},
	}
	// The one reader of stdin: the program reads through it, and so does the plain
	// prompt after a fallback, never both at once.
	gate := newStdinGate(p.Stdin)
	b.plainResponder = installplain.Responder{Out: con.Stdout, In: gate.handOverLater()}
	broker := installflow.NewBroker(hub, b, req.AssumeYes)

	forced := make(chan struct{})
	var forceOnce sync.Once
	intr := &interrupter{cancel: cancel, force: func() {
		forceOnce.Do(func() {
			close(forced)
			// Run returns, with the terminal restored, before the exit. v2's Kill shuts the
			// program down synchronously, so it is never called on a goroutine the shutdown
			// waits for (the event loop, the renderer).
			go prog.Kill()
		})
	}}

	started := make(chan struct{})
	var startOnce sync.Once
	start := func() { startOnce.Do(func() { close(started) }) }

	// A screen that stops accepting writes is a failed renderer. Bubble Tea discards its
	// write errors, so they are caught here: the program is stopped and the run falls back.
	var screenFailed atomic.Bool
	screen := &latchWriter{w: p.Stdout, failed: func() {
		if screenFailed.CompareAndSwap(false, true) {
			go prog.Kill() // this write is the renderer's, which the shutdown waits for
		}
	}}
	opts := append([]tea.ProgramOption{
		tea.WithInput(gate.programInput()), tea.WithOutput(screenOutput(screen, p.Stdout)),
		// The root command owns the signals; this presenter dedupes them with Ctrl+C.
		tea.WithoutSignalHandler(),
		// The size the probe saw. On a terminal Bubble Tea reads the real size itself and
		// this is only its starting point; it never draws into a zero-sized screen.
		tea.WithWindowSize(sizeOr(p.Caps.Width, 80), sizeOr(p.Caps.Height, 24)),
	}, p.options...)
	prog = tea.NewProgram(newModel(p.Caps, intr, start, hub, hub.Redact), opts...)

	// OS signals while the screen is up. The root context hears the first one too; this
	// channel is what keeps the SECOND from killing the process with the terminal still
	// raw, and what counts a signal and a keypress as one stream of interrupts.
	sigs := make(chan os.Signal, 2)
	stopSignals := p.notifySignals(sigs)
	sigDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-sigs:
				intr.interrupt()
			case <-sigDone:
				return
			}
		}
	}()
	go func() {
		select {
		case <-runCtx.Done():
			prog.Send(stoppingMsg{})
		case <-b.done:
		}
	}()

	engineDone := make(chan engineResult, 1)
	go func() {
		// The engine starts when the screen says so (after the splash), or at once in
		// plain output if the screen never came up.
		select {
		case <-started:
		case <-b.done:
		}
		var res engineResult
		defer func() {
			if r := recover(); r != nil {
				res.panicked, res.stack = r, debug.Stack()
				go prog.Kill()
			}
			engineDone <- res
		}()
		res.sum, res.err = hub.Run(runCtx, e, req, installflow.Runtime{
			Prompts: broker, Preflight: preflight, Console: con, ObserveTools: true})
	}()

	saved := saveTerminal(p.Stdin, p.Stdout)
	_, perr := prog.Run()
	close(sigDone)
	stopSignals()
	// 🔴 BUBBLE TEA DOES NOT RESTORE ON EVERY RETURN. When its input reader cannot start it
	// returns after it has put the terminal in raw mode, and a screen it can no longer
	// write never sees its own restore sequences. The modes saved before it ran are put
	// back here whatever happened; after any abnormal end the screen is also reset,
	// through stderr when stdout is the stream that failed.
	saved.restore()
	if perr != nil || screenFailed.Load() || isClosed(forced) {
		reset := p.Stdout
		if screenFailed.Load() {
			reset = nil
			if p.Caps.ErrorTTY {
				reset = p.Stderr
			}
		}
		if reset != nil {
			_, _ = io.WriteString(reset, screenReset)
		}
	}

	select {
	case <-forced:
		fmt.Fprintln(p.Stderr, "dcctl: interrupted again; exiting without waiting for the operation to stop")
		p.exitProcess(1)
		return errForced
	default:
	}

	_, finished := hub.Final()
	if perr != nil || !finished {
		// The screen is gone. If the run is not over, the same run carries on in plain
		// output; it is never started again. A panic in the engine also lands here (its
		// run is finished), and is raised below.
		gate.handOver()
		// 🔴 ON A WINDOWS CONSOLE THE PROGRAM'S READER MAY STILL BE LIVE. Bubble Tea reads
		// console records past the gate, and a killed program does not wait for that
		// reader; a plain prompt reading beside it could lose its answer to it or hang.
		// There is no way to join it, so the fallback asks nothing: a question fails
		// closed, as it does with no terminal to answer it.
		if p.consoleBypassesGate || consoleReadsPastGate(p.Stdin) {
			gate.refuse(installflow.ErrPromptUnavailable)
		}
		b.fallBack()
	}
	close(b.done)
	if !finished {
		reason := "it closed before the run finished"
		if perr != nil {
			reason = perr.Error()
		}
		fmt.Fprintf(p.Stderr, "dcctl: the terminal UI stopped (%s); continuing with plain output\n", reason)
		if gate.refusal() != nil {
			fmt.Fprintln(p.Stderr, "dcctl: questions cannot be answered after the terminal UI stopped on this console; "+
				"re-run with --ui=plain to answer them")
		}
	}

	res := <-engineDone
	if res.panicked != nil {
		_, _ = p.Stderr.Write(res.stack)
		panic(res.panicked)
	}
	<-hub.Done()
	if p.onFinish != nil {
		p.onFinish(hub)
	}
	if streamsDropped {
		fmt.Fprintln(p.Stderr, "dcctl: some output from a child process was still arriving when the run ended and was not shown")
	}
	if !b.plain.Load() {
		writeExitSummary(p.Stdout, res.sum)
	}
	// The command prints this error after the screen has closed, and a tool's
	// diagnostics are often appended to it: it is redacted, as an event is.
	if res.err == nil && !res.sum.Succeeded() {
		return hub.RedactError(errIncomplete)
	}
	return hub.RedactError(res.err)
}

func (p Presenter) notifySignals(c chan<- os.Signal) func() {
	if p.notify != nil {
		return p.notify(c)
	}
	signal.Notify(c, interruptSignals...)
	return func() { signal.Stop(c) }
}

func (p Presenter) exitProcess(code int) {
	if p.exit != nil {
		p.exit(code)
		return
	}
	os.Exit(code)
}

// writeExitSummary is what stays in the scrollback once the screen is gone: the verdict,
// where it stopped, and what to run next. It carries no secret: every field is one of the
// summary's safe values.
func writeExitSummary(w io.Writer, s installflow.Summary) {
	fmt.Fprintf(w, "dcctl install: %s\n", outcomeWord(s.Outcome))
	if s.FailedStep != "" {
		fmt.Fprintf(w, "stopped at: %s\n", s.FailedStep)
	}
	for _, n := range s.Next {
		argv := make([]string, len(n.Argv))
		for i, a := range n.Argv {
			argv[i] = a.String()
		}
		fmt.Fprintf(w, "next: %s\n", strings.Join(argv, " "))
	}
}

func sizeOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// screenReset puts a terminal back the way a shell expects it after the program: the cursor
// shown, the alternate screen left, bracketed paste and the extended keyboard modes off,
// and the character attributes reset. Each is harmless if it was never set.
const screenReset = "\x1b[?25h\x1b[?1049l\x1b[?2004l\x1b[>4m\x1b[<u\x1b[0m"

// latchWriter reports its writer's first failure, a panic included.
//
// 🔴 BUBBLE TEA WRITES THE SCREEN FROM A REDRAW GOROUTINE THAT RECOVERS NOTHING. A panic
// there ends the process without Run returning, so the terminal would never be restored.
// Every byte the program writes passes through here, so a panic in writing it is caught
// and becomes a failed screen: Run returns, the terminal is restored, the run falls back.
// What this cannot reach is a panic in Bubble Tea's own redraw code before it writes; the
// event loop (Update, View) is recovered by Bubble Tea itself.
func (l *latchWriter) Write(p []byte) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = 0, fmt.Errorf("writing the screen panicked: %v", r)
			l.failed()
		}
	}()
	n, err = l.w.Write(p)
	if err != nil {
		l.failed()
	}
	return n, err
}

type latchWriter struct {
	w      io.Writer
	failed func()
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}
