// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

const (
	hideCursor = "\x1b[?25l"
	showCursor = "\x1b[?25h"
	altOn      = "\x1b[?1049h"
	altOff     = "\x1b[?1049l"
)

// assertRestored checks the screen was handed back: the cursor shown after it was last
// hidden, and the alternate screen left after it was last entered, both before marker.
func assertRestored(t *testing.T, out, marker string) {
	t.Helper()
	end := len(out)
	if marker != "" {
		end = strings.LastIndex(out, marker)
		if end < 0 {
			t.Fatalf("%q never printed; output:\n%q", marker, out)
		}
	}
	before := out[:end]
	if strings.LastIndex(before, showCursor) < strings.LastIndex(before, hideCursor) {
		t.Errorf("the cursor was not shown again before %q:\n%q", marker, out)
	}
	if strings.LastIndex(before, altOff) < strings.LastIndex(before, altOn) {
		t.Errorf("the alternate screen was not left before %q:\n%q", marker, out)
	}
}

func TestProgressOnlyRunExitsOnCompletion(t *testing.T) {
	caps := tuiCaps(true)
	// A full-size screen, where the log pane is open without a key.
	caps.Width, caps.Height, caps.SizeKnown, caps.Layout = 120, 40, true, terminal.Full
	h := newHarness(t, caps)
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if !rt.ObserveTools {
			// The screen draws measured progress, so it asks the engine to observe its
			// tools (OpenTofu's -json apply) for it.
			return installflow.Summary{}, errors.New("the terminal UI did not turn on ObserveTools")
		}
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		fmt.Fprintln(rt.Console.Stdout, "words from the engine")
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	assertRestored(t, out, "dcctl install: succeeded")
	// The engine's words went to the log ring, drawn on screen, never straight to stdout.
	if !strings.Contains(out, "dcctl   words from the engine") {
		t.Errorf("the engine's output never reached the screen:\n%q", out)
	}
	if strings.Contains(out, "\nwords from the engine") {
		t.Errorf("the engine wrote to stdout under the screen:\n%q", out)
	}
}

func TestKindPromptIsAnsweredOnScreen(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{{"y", true}, {"n", false}, {"\r", false}} {
		t.Run(fmt.Sprintf("%q", tc.key), func(t *testing.T) {
			h := newHarness(t, tuiCaps(false))
			var got atomic.Value
			done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
				if err := startRun(rt); err != nil {
					return installflow.Summary{}, err
				}
				ans, _ := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
					Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
					Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}, YesResolvable: true})
				got.Store(ans.Confirmed)
				return succeeded, endRun(rt)
			}), installflow.Request{Operation: installflow.OperationInstall})
			waitFor(t, "the question", h.out, screenShows(h.out, `No kind cluster "devicechain" found. Create it now? [y/N]`))
			h.press(tc.key)
			waitFor(t, "the verdict", h.out, screenShows(h.out, "Press Enter to exit."))
			if strings.Contains(h.out.String(), "[y/N]: ") {
				t.Fatal("the question was asked by a second reader of stdin, not on screen")
			}
			if c, _ := got.Load().(bool); c != tc.want {
				t.Fatalf("answer %q confirmed=%v, want %v", tc.key, c, tc.want)
			}
			// A finished run waits on its result screen until it is dismissed.
			select {
			case err := <-done:
				t.Fatalf("the result screen closed by itself (%v)", err)
			case <-time.After(100 * time.Millisecond):
			}
			h.press("\r")
			if err := h.result(done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDismissingAFailureKeepsTheError(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	cause := errors.New("the operator rollout timed out")
	done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
			Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
			PartialState: installflow.EffectMayHaveChanged}, cause
	}), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install failed."))
	h.press("q")
	if err := h.result(done); !errors.Is(err, cause) {
		t.Fatalf("closing the failure screen returned %v, want the engine's error", err)
	}
	assertRestored(t, h.out.String(), "dcctl install: failed")
}

func TestSplashHoldsTheRunUntilItEnds(t *testing.T) {
	caps := tuiCaps(false)
	caps.Splash = terminal.TextHeader
	h := newHarness(t, caps)
	var startedAt atomic.Int64
	began := time.Now()
	done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		startedAt.Store(int64(time.Since(began)))
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the splash", h.out, screenShows(h.out, "Prepare a Kubernetes cluster"))
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	if d := time.Duration(startedAt.Load()); d < splashHold {
		t.Errorf("the engine started %v in, during the %v splash", d, splashHold)
	}
	h.press("\r")
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
}

func TestASkippedSplashStartsTheRun(t *testing.T) {
	caps := tuiCaps(false)
	caps.Splash = terminal.TextHeader
	h := newHarness(t, caps)
	var asked atomic.Bool
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		asked.Store(true)
		ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
			Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}})
		if err != nil || !ans.Confirmed {
			return cancelled, fmt.Errorf("not confirmed: %v", err)
		}
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the splash", h.out, screenShows(h.out, "press any key"))
	// The skip key is consumed: the question that follows still waits for its own answer.
	h.press("y")
	waitFor(t, "the question", h.out, screenShows(h.out, "[y/N]"))
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(h.out.String(), "Install succeeded.") {
		t.Fatal("the key that skipped the splash also answered the question")
	}
	h.press("y")
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	h.press("\r")
	if err := h.result(done); err != nil || !asked.Load() {
		t.Fatalf("err=%v asked=%v", err, asked.Load())
	}
}

// stubbornEngine stands for a tool still stopping gracefully: it notes its context was
// cancelled and then keeps waiting, as tofu does for up to its graceful-stop budget.
func stubbornEngine(cancels *atomic.Int32) installflow.Engine {
	return engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		<-ctx.Done()
		cancels.Add(1)
		select {} // never settles
	})
}

func TestFirstCtrlCCancelsOnceAndTheScreenStays(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	var cancels atomic.Int32
	done := h.run(stubbornEngine(&cancels), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the run", h.out, screenShows(h.out, "Resolve the target cluster"))
	h.press("\x03")
	waitFor(t, "the cancel", h.out, func() bool { return cancels.Load() == 1 })
	waitFor(t, "the stopping notice", h.out, screenShows(h.out, "Interrupt received."))
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the first Ctrl+C ended the presenter (%v)", err)
	case code := <-h.exits:
		t.Fatalf("the first Ctrl+C exited the process (%d)", code)
	default:
	}
	if strings.Contains(h.out.String(), altOff) {
		t.Fatal("the first Ctrl+C took the screen down before the run settled")
	}
	h.press("\x03")
	if err := h.result(done); !errors.Is(err, errForced) {
		t.Fatalf("second Ctrl+C: %v", err)
	}
	select {
	case code := <-h.exits:
		if code != 1 {
			t.Fatalf("forced exit status %d, want 1", code)
		}
	default:
		t.Fatal("the second Ctrl+C did not exit")
	}
	assertRestored(t, h.out.String(), "interrupted again")
	if n := cancels.Load(); n != 1 {
		t.Fatalf("the engine saw %d cancels", n)
	}
}

func TestASignalThenCtrlCIsTheSecondInterrupt(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	var cancels atomic.Int32
	done := h.run(stubbornEngine(&cancels), installflow.Request{Operation: installflow.OperationInstall})
	var sigs chan<- os.Signal
	select {
	case sigs = <-h.sigs:
	case <-time.After(10 * time.Second):
		t.Fatal("the presenter never listened for signals")
	}
	waitFor(t, "the run", h.out, screenShows(h.out, "Resolve the target cluster"))
	sigs <- os.Interrupt
	waitFor(t, "the cancel", h.out, func() bool { return cancels.Load() == 1 })
	waitFor(t, "the stopping notice", h.out, screenShows(h.out, "Interrupt received."))
	select {
	case code := <-h.exits:
		t.Fatalf("one signal exited the process (%d)", code)
	default:
	}
	h.press("\x03")
	if err := h.result(done); !errors.Is(err, errForced) {
		t.Fatalf("Ctrl+C after a signal: %v", err)
	}
	if code := <-h.exits; code != 1 {
		t.Fatalf("forced exit status %d, want 1", code)
	}
	assertRestored(t, h.out.String(), "interrupted again")
}

func TestCtrlCWaitsForTheEngineToSettle(t *testing.T) {
	h := newHarness(t, tuiCaps(true)) // even progress-only: the verdict, not the key, ends it
	settle := make(chan struct{})
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		<-ctx.Done()
		<-settle // the graceful stop takes a while
		return cancelled, ctx.Err()
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
	waitFor(t, "the run", h.out, screenShows(h.out, "Resolve the target cluster"))
	h.press("\x03")
	waitFor(t, "the stopping notice", h.out, screenShows(h.out, "Interrupt received."))
	select {
	case err := <-done:
		t.Fatalf("the presenter returned before the engine settled (%v)", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(settle)
	if err := h.result(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled run returned %v", err)
	}
	select {
	case code := <-h.exits:
		t.Fatalf("a settled cancel exited the process (%d)", code)
	default:
	}
	assertRestored(t, h.out.String(), "dcctl install: cancelled")
}

func TestAnEnginePanicIsRaisedAfterTheRestore(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_ = h.p.Run(context.Background(), engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
			if err := startRun(rt); err != nil {
				return installflow.Summary{}, err
			}
			panic("engine exploded")
		}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}, nil)
	}()
	select {
	case r := <-recovered:
		if r != "engine exploded" {
			t.Fatalf("recovered %v", r)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("no panic; output:\n%q", h.out.String())
	}
	// The stack is printed after the screen is handed back.
	assertRestored(t, h.out.String(), "goroutine ")
}

// failingWriter records what it is given until told to fail, then fails every write.
type failingWriter struct {
	syncBuffer
	failing atomic.Bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.failing.Load() {
		return 0, errors.New("write /dev/stdout: input/output error")
	}
	return f.syncBuffer.Write(p)
}

// A screen that cannot be written is a failed renderer: the program stops, the run goes on
// in plain output, and the terminal is put back through stderr, which still works.
func TestAStdoutThatStopsAcceptingWritesIsAFailedRenderer(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	h.p.Caps.ErrorTTY = true
	out := &failingWriter{}
	errs := &syncBuffer{}
	h.p.Stdout, h.p.Stderr = out, errs
	next := make(chan struct{})
	done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		<-next
		fmt.Fprintln(rt.Console.Stdout, "a line that redraws the screen")
		<-next
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
	waitFor(t, "the run", &out.syncBuffer, screenShows(&out.syncBuffer, "Resolve the target cluster"))
	out.failing.Store(true)
	next <- struct{}{}
	waitFor(t, "the fallback", errs, screenShows(errs, "continuing with plain output"))
	next <- struct{}{}
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs.String(), altOff) || !strings.Contains(errs.String(), showCursor) {
		t.Fatalf("the terminal was not put back through stderr:\n%q", errs.String())
	}
}

// When the renderer dies at a question, the program's input loop is still waiting on stdin
// and would take the answer first. The answer must reach the plain prompt all the same.
func TestAnAnswerAfterARendererFailureReachesThePlainPrompt(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	in := make(fifoReader)
	h.p.Stdin = in
	h.p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if _, ok := msg.(promptMsg); ok {
			panic("renderer failed")
		}
		return msg
	})}
	var confirmed atomic.Bool
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
			Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}})
		if err != nil || !ans.Confirmed {
			return cancelled, fmt.Errorf("not confirmed: %v", err)
		}
		confirmed.Store(true)
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the plain question", h.out, screenShows(h.out, "Create it now? [y/N]: "))
	time.Sleep(100 * time.Millisecond) // the dead program's input loop is waiting too
	select {
	case in <- []byte("y\n"):
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read stdin")
	}
	if err := h.result(done); err != nil || !confirmed.Load() {
		t.Fatalf("the answer did not reach the plain prompt: err=%v confirmed=%v", err, confirmed.Load())
	}
}

// panickingWriter records until told to panic, then panics on every write.
type panickingWriter struct {
	syncBuffer
	panicking atomic.Bool
}

func (w *panickingWriter) Write(p []byte) (int, error) {
	if w.panicking.Load() {
		panic("the screen's writer exploded")
	}
	return w.syncBuffer.Write(p)
}

// Bubble Tea writes the screen from a redraw goroutine of its own, with no recovery: a
// panic in that write would end the process without Run ever returning, so the terminal
// would never be restored. The presenter's writer turns it into a failed screen instead.
func TestAPanicWritingTheScreenIsAFailedRenderer(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	h.p.Caps.ErrorTTY = true
	out := &panickingWriter{}
	errs := &syncBuffer{}
	h.p.Stdout, h.p.Stderr = out, errs
	next := make(chan struct{})
	done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		<-next
		fmt.Fprintln(rt.Console.Stdout, "a line that redraws the screen")
		<-next
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
	waitFor(t, "the run", &out.syncBuffer, screenShows(&out.syncBuffer, "Resolve the target cluster"))
	out.panicking.Store(true)
	next <- struct{}{}
	waitFor(t, "the fallback", errs, screenShows(errs, "continuing with plain output"))
	out.panicking.Store(false)
	next <- struct{}{}
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
}

// On a Windows console Bubble Tea reads console records itself, past the stdin gate, and a
// killed program does not wait for that reader. After a renderer failure there may still
// be a reader on the console, so the plain prompt must not read beside it: the question
// fails closed, and the operator is told how to answer it.
func TestAConsoleReaderThatBypassesTheGateFailsTheFallbackPromptClosed(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	in := make(fifoReader)
	h.p.Stdin = in
	h.p.consoleBypassesGate = true
	h.p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if _, ok := msg.(promptMsg); ok {
			panic("renderer failed")
		}
		return msg
	})}
	asked := make(chan error, 1)
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		_, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
			Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}})
		asked <- err
		return cancelled, fmt.Errorf("not confirmed: %w", err)
	}), installflow.Request{Operation: installflow.OperationInstall})
	select {
	case err := <-asked:
		if !errors.Is(err, installflow.ErrPromptUnavailable) {
			t.Fatalf("the fallback question = %v, want it unavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the fallback question read the console beside the program's reader; output:\n%q", h.out.String())
	}
	if err := h.result(done); err == nil {
		t.Fatal("an unanswerable question let the run succeed")
	}
	if !strings.Contains(h.out.String(), "--ui=plain") {
		t.Fatalf("the operator was not told how to answer:\n%q", h.out.String())
	}
}

const canarySecret = "s3cr3t-canary-0123456789abcdef"

// The engine's error is printed by the command after the screen closes, and a tool's
// diagnostics are often appended to it: it is redacted, as the plain and JSON
// presenters redact it.
func TestTheEnginesErrorIsRedacted(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.AddSecret(canarySecret); err != nil {
			return installflow.Summary{}, err
		}
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return installflow.Summary{Outcome: installflow.RunFailed, InstallRecord: installflow.RecordUnknown,
			Readiness: installflow.ReadinessUnknown, Claim: installflow.ClaimUnknown,
			PartialState: installflow.EffectMayHaveChanged}, fmt.Errorf("tofu apply: Error: auth with %s refused", canarySecret)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err == nil {
		t.Fatal("a failed run returned no error")
	}
	if strings.Contains(err.Error(), canarySecret) {
		t.Fatalf("the returned error carries a registered secret: %v", err)
	}
}

// While the screen is up, nothing in the process may write to the terminal beside it:
// a kubeconfig exec credential plugin is started with the process's own stderr, and
// whatever it says goes to the log pane, redacted, instead of over the screen. And it
// may not read stdin beside the program: the process's stdin is the null device.
func TestTheProcessStreamsBelongToTheScreenWhileItIsUp(t *testing.T) {
	caps := tuiCaps(true)
	// A full-size screen, where the log pane is open without a key.
	caps.Width, caps.Height, caps.SizeKnown, caps.Layout = 120, 40, true, terminal.Full
	h := newHarness(t, caps)
	h.p.OwnProcessStreams = true
	realStdin, realStderr := os.Stdin, os.Stderr
	var sawStdin atomic.Value
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.AddSecret(canarySecret); err != nil {
			return installflow.Summary{}, err
		}
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		fmt.Fprintf(os.Stderr, "exec plugin: token %s expired\n", canarySecret)
		n, rerr := os.Stdin.Read(make([]byte, 8))
		sawStdin.Store(fmt.Sprintf("same=%v n=%d err=%v", os.Stdin == realStdin, n, rerr))
		time.Sleep(200 * time.Millisecond) // the line reaches the pane
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err != nil {
		t.Fatal(err)
	}
	if os.Stdin != realStdin || os.Stderr != realStderr {
		t.Fatal("the process's streams were not given back")
	}
	if got := sawStdin.Load(); got != "same=false n=0 err=EOF" {
		t.Fatalf("during the run the process's stdin was %v; want the null device", got)
	}
	out := h.out.String()
	if !strings.Contains(out, "exec plugin: token") {
		t.Fatalf("a write to the process's stderr did not reach the log pane:\n%q", out)
	}
	if strings.Contains(out, canarySecret) {
		t.Fatalf("a write to the process's stderr reached the screen unredacted:\n%q", out)
	}
}

// A child's last words often have no newline. They are flushed into the log before the
// run's summary closes it, not dropped with the hub already finished.
func TestAChildsTrailingPartialLineReachesTheLog(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	h.p.OwnProcessStreams = true
	var logged atomic.Value
	h.p.onFinish = func(hub *installflow.Hub) {
		var lines []string
		for _, l := range hub.Logs().Lines {
			lines = append(lines, l.Text.String())
		}
		logged.Store(strings.Join(lines, "|"))
	}
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		fmt.Fprint(os.Stderr, "plugin: last words with no newline")
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := logged.Load().(string); !strings.Contains(got, "plugin: last words with no newline") {
		t.Fatalf("the trailing line never reached the log: %q", got)
	}
}

// After a renderer failure the engine runs on, and so may a credential plugin that
// client-go set up with the streams it found: they stay the takeover's own, open and
// unchanged, until the engine returns. What the plugin says is printed, redacted, on
// stderr with the rest of the plain output.
func TestTheStreamsStayTakenWhenTheRunFallsBackToPlain(t *testing.T) {
	h := newHarness(t, tuiCaps(true))
	h.p.OwnProcessStreams = true
	errs := &syncBuffer{}
	h.p.Stderr = errs
	h.p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if e, ok := msg.(eventMsg); ok {
			if l, ok := e.env.Payload.(installflow.LogLine); ok && strings.Contains(l.Text.String(), "kill the renderer") {
				panic("renderer failed")
			}
		}
		return msg
	})}
	realStdin, realStderr := os.Stdin, os.Stderr
	var after atomic.Value
	err := h.result(h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.AddSecret(canarySecret); err != nil {
			return installflow.Summary{}, err
		}
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		cachedIn, cachedErr := os.Stdin, os.Stderr // as client-go's authenticator holds them
		fmt.Fprintln(rt.Console.Stdout, "kill the renderer")
		waitFor(t, "the fallback", errs, screenShows(errs, "continuing with plain output"))
		_, werr := fmt.Fprintf(cachedErr, "plugin after the fallback: %s\n", canarySecret)
		n, rerr := cachedIn.Read(make([]byte, 8))
		after.Store(fmt.Sprintf("write=%v read=%d,%v same=%v,%v", werr, n, rerr, os.Stdin == cachedIn, os.Stderr == cachedErr))
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true}))
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Load(); got != "write=<nil> read=0,EOF same=true,true" {
		t.Fatalf("after the fallback the streams were %v; want the takeover's, open and unchanged", got)
	}
	if os.Stdin != realStdin || os.Stderr != realStderr {
		t.Fatal("the process's streams were not given back after the run")
	}
	waitFor(t, "the plugin's line", errs, screenShows(errs, "plugin after the fallback: "))
	if strings.Contains(errs.String(), canarySecret) {
		t.Fatalf("the plugin's line was printed unredacted:\n%q", errs.String())
	}
}

func TestInterrupterCancelsOnceThenForcesOnce(t *testing.T) {
	var cancels, forces int
	i := &interrupter{cancel: func() { cancels++ }, force: func() { forces++ }}
	if !i.interrupt() {
		t.Fatal("the first interrupt was not first")
	}
	if cancels != 1 || forces != 0 {
		t.Fatalf("after one: cancels=%d forces=%d", cancels, forces)
	}
	if i.interrupt() {
		t.Fatal("the second interrupt claimed to be first")
	}
	i.interrupt()
	if cancels != 1 || forces != 1 || i.count() != 3 {
		t.Fatalf("after three: cancels=%d forces=%d count=%d", cancels, forces, i.count())
	}
}
