// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

// countingInterrupter counts what reaches the interrupter's two outcomes.
func countingInterrupter(n int) (*interrupter, *atomic.Int32, *atomic.Int32) {
	var cancels, forces atomic.Int32
	return &interrupter{n: n, cancel: func() { cancels.Add(1) }, force: func() { forces.Add(1) }}, &cancels, &forces
}

func quits(cmd tea.Cmd) bool {
	for _, m := range messages(cmd) {
		if _, ok := m.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

// 🔴 CTRL+C ON A FINISHED SCREEN IS THE EXIT KEY, NOT AN INTERRUPT. The run is over:
// nothing is left to stop, and a counted press would make the second one (or the first
// after an interrupt during the run, or a signal) force the process out with a status
// that is not the run's, before the exit summary is written.
func TestCtrlCOnAFinishedScreenLeavesWithoutForcing(t *testing.T) {
	failed := cancelled
	failed.Outcome = installflow.RunFailed
	for _, tc := range []struct {
		name      string
		sum       installflow.Summary
		continues bool
		before    int // interrupts received during the run
	}{
		{name: "succeeded", sum: succeeded},
		{name: "failed", sum: failed},
		{name: "cancelled by an interrupt during the run", sum: cancelled, before: 1},
		{name: "setup's cluster stage, interrupted during the run", sum: cancelled, continues: true, before: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intr, _, forces := countingInterrupter(tc.before)
			var m tea.Model = newModel(tuiCaps(false), intr, func() {}, nil, nil,
				modelOptions{op: installflow.OperationInstall, continues: tc.continues})
			m, _ = m.Update(eventMsg{env: installflow.Envelope{Payload: tc.sum}})
			for press := 1; press <= 2; press++ {
				var cmd tea.Cmd
				m, cmd = m.Update(ctrlC)
				if !quits(cmd) {
					t.Fatalf("Ctrl+C %d on the finished screen did not leave", press)
				}
			}
			if n := forces.Load(); n != 0 {
				t.Fatalf("Ctrl+C on the finished screen forced the process out (%d)", n)
			}
			if got := intr.count(); got != tc.before {
				t.Fatalf("Ctrl+C on the finished screen was counted: %d interrupts, want %d", got, tc.before)
			}
		})
	}
}

// Setup's cluster stage still learns of a Ctrl+C on its finished screen, once: it is the
// operator declining the instance that would follow.
func TestCtrlCOnSetupsFinishedClusterStageIsCountedOnce(t *testing.T) {
	intr, cancels, forces := countingInterrupter(0)
	var m tea.Model = newModel(tuiCaps(false), intr, func() {}, nil, nil,
		modelOptions{op: installflow.OperationInstall, continues: true})
	m, _ = m.Update(eventMsg{env: installflow.Envelope{Payload: succeeded}})
	for press := 1; press <= 2; press++ {
		var cmd tea.Cmd
		m, cmd = m.Update(ctrlC)
		if !quits(cmd) {
			t.Fatalf("Ctrl+C %d did not leave", press)
		}
	}
	if intr.count() != 1 || cancels.Load() != 1 || forces.Load() != 0 {
		t.Fatalf("interrupts %d, cancels %d, forces %d; want 1, 1, 0", intr.count(), cancels.Load(), forces.Load())
	}
}

// A failed instance run's verdict is left by Ctrl+C the same way: setup then says the
// cluster stays installed and how to create the instance.
func TestCtrlCOnAFailedInstanceVerdictLeavesWithoutForcing(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	intr, _, forces := countingInterrupter(0)
	d.m.intr = intr
	d.toReview()
	d.settle()
	d.keys("tab", "enter")
	failed := cancelled
	failed.Outcome = installflow.RunFailed
	d.m.logs = emptyLogs{}
	d.send(eventMsg{env: installflow.Envelope{Payload: failed}})
	d.send(ctrlC)
	if !d.quit {
		t.Fatal("Ctrl+C on a failed instance verdict did not leave")
	}
	d.quit = false
	d.send(ctrlC)
	if !d.quit || forces.Load() != 0 || intr.count() != 0 {
		t.Fatalf("a second Ctrl+C: quit %v, forces %d, interrupts %d", d.quit, forces.Load(), intr.count())
	}
}

// On the real presenter: a signal on the finished screen, then Ctrl+C. The signal is
// the first interrupt; the key must not be the second.
func TestASignalThenCtrlCOnTheFinishedScreenKeepsTheRunsStatus(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall})
	var sigs chan<- os.Signal
	select {
	case sigs = <-h.sigs:
	case <-time.After(10 * time.Second):
		t.Fatal("the presenter never listened for signals")
	}
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Press Enter to exit."))
	sigs <- os.Interrupt
	time.Sleep(100 * time.Millisecond)
	h.press("\x03")
	if err := h.result(done); err != nil {
		t.Fatalf("a succeeded install left with Ctrl+C returned %v", err)
	}
	select {
	case code := <-h.exits:
		t.Fatalf("Ctrl+C on the finished screen forced an exit (%d)", code)
	default:
	}
	out := h.out.String()
	if strings.Contains(out, "interrupted again") || !strings.Contains(out, "dcctl install: succeeded") {
		t.Fatalf("the exit summary was not written:\n%q", out)
	}
}

// --- a one-time disclosure after a fallback to plain output ---

func seedOffer(value string) installflow.RevealOffer {
	return installflow.RevealOffer{Subject: installflow.RevealSuperuserSeed, Reveal: installflow.NewReveal([]byte(value)),
		PlainLine: func(w io.Writer, v []byte) { fmt.Fprintf(w, "password: %s\n", v) }}
}

// 🔴 NOTHING TAKES FROM THE INBOX AFTER A FALLBACK, so the fallback shows what it holds,
// and what is offered later is shown at once.
func TestAFallbackShowsTheOneTimeDisclosuresInPlainOutput(t *testing.T) {
	out := &syncBuffer{}
	inbox := &RevealInbox{}
	r := &fallbackReveals{inbox: inbox, w: out}
	b := &bridge{reveals: r}
	r.Offer(seedOffer("before-the-fallback"))
	if out.String() != "" {
		t.Fatalf("a disclosure was printed while the screen was up:\n%s", out)
	}
	b.fallBack()
	r.Offer(seedOffer("after-the-fallback"))
	got := out.String()
	for _, w := range []string{"password: before-the-fallback", "password: after-the-fallback", "shown here, once"} {
		if !strings.Contains(got, w) {
			t.Errorf("plain output lacks %q:\n%s", w, got)
		}
	}
	if strings.Count(got, "shown here, once") != 1 {
		t.Errorf("only the disclosure the screen never showed is introduced:\n%s", got)
	}
	if len(inbox.Take()) != 0 || r.wasLost() {
		t.Fatal("a disclosure was left in the inbox or counted lost")
	}
}

// A disclosure plain output cannot show is never a silent success.
func TestADisclosurePlainOutputCannotShowIsLost(t *testing.T) {
	for name, tc := range map[string]struct {
		w     io.Writer
		offer installflow.RevealOffer
	}{
		"the writer fails": {w: failWriter{}, offer: seedOffer("x")},
		"no plain form":    {w: io.Discard, offer: installflow.RevealOffer{Reveal: installflow.NewReveal([]byte("x"))}},
		"a short write":    {w: shortWriter{}, offer: seedOffer("x")},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fallbackReveals{inbox: &RevealInbox{}, w: tc.w}
			r.fallBack()
			r.Offer(tc.offer)
			if !r.wasLost() {
				t.Fatal("a disclosure that was not shown was not counted lost")
			}
			if !tc.offer.Reveal.Consumed() {
				t.Fatal("the reveal was left unconsumed")
			}
		})
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) {
	return 0, errors.New("write /dev/stdout: input/output error")
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

// 🔴 AFTER A FALLBACK A SECRET IS NEVER READ FROM os.Stdin (the null device while the
// engine runs), nothing typed before the question becomes its answer, and where that
// cannot be made sure of it fails closed as a prompt nobody can answer.
func TestTheFallbacksHiddenReadFailsClosed(t *testing.T) {
	t.Run("not a terminal", func(t *testing.T) {
		g := newStdinGate(strings.NewReader("hunter2\n"))
		if _, err := g.plainReadHidden(); !errors.Is(err, installflow.ErrPromptUnavailable) {
			t.Fatalf("a hidden read of a pipe: %v", err)
		}
	})
	t.Run("a console the program may still read", func(t *testing.T) {
		g := newStdinGate(strings.NewReader("hunter2\n"))
		g.refuse(installflow.ErrPromptUnavailable)
		if _, err := g.plainReadHidden(); !errors.Is(err, installflow.ErrPromptUnavailable) {
			t.Fatalf("a refused gate: %v", err)
		}
	})
	// From here stdin is a terminal, whose hidden read answers "hunter2", and whose
	// typed-ahead queue holds what queued says.
	saveRead, saveDiscard := terminalHiddenRead, discardTypeAhead
	t.Cleanup(func() { terminalHiddenRead, discardTypeAhead = saveRead, saveDiscard })
	terminalHiddenRead = func(io.Reader) (func() ([]byte, error), bool) {
		return func() ([]byte, error) { return []byte("hunter2"), nil }, true
	}
	var queued bool
	var discardErr error
	discards := 0
	discardTypeAhead = func(io.Reader) (bool, error) { discards++; return queued, discardErr }
	ask := func(g *stdinGate) (string, string, error) {
		notice := &syncBuffer{}
		g.notice = notice
		got, err := g.plainReadHidden()
		return string(got), notice.String(), err
	}
	t.Run("a terminal", func(t *testing.T) {
		queued, discardErr, discards = false, nil, 0
		got, notice, err := ask(newStdinGate(strings.NewReader("")))
		if err != nil || got != "hunter2" || notice != "" || discards != 1 {
			t.Fatalf("a hidden read of the terminal: %q, %v, notice %q, discards %d", got, err, notice, discards)
		}
	})
	t.Run("input queued in the terminal", func(t *testing.T) {
		queued, discardErr, discards = true, nil, 0
		got, notice, err := ask(newStdinGate(strings.NewReader("")))
		if err != nil || got != "hunter2" || !strings.Contains(notice, "discarded") || discards != 1 {
			t.Fatalf("typed-ahead input: %q, %v, notice %q, discards %d", got, err, notice, discards)
		}
	})
	t.Run("a queue that cannot be flushed", func(t *testing.T) {
		queued, discardErr = true, errors.New("inappropriate ioctl for device")
		if _, _, err := ask(newStdinGate(strings.NewReader(""))); !errors.Is(err, installflow.ErrPromptUnavailable) {
			t.Fatalf("an unflushable queue was read: %v", err)
		}
	})
	t.Run("keys typed to the screen", func(t *testing.T) {
		queued, discardErr = false, nil
		g := newStdinGate(strings.NewReader(""))
		g.stash = []byte("hun")
		stash := g.stash
		got, notice, err := ask(g)
		if err != nil || got != "hunter2" || !strings.Contains(notice, "discarded") {
			t.Fatalf("a stash: %q, %v, notice %q", got, err, notice)
		}
		if string(stash) != "\x00\x00\x00" || len(g.stash) != 0 {
			t.Fatalf("the keys typed to the screen were not wiped: %q", stash)
		}
	})
}

// The presenter's plain prompt reads a secret through the gate: an engine that asks one
// after a fallback gets the gate's answer (here, refused: stdin is a pipe), not a read of
// the process's os.Stdin.
func TestTheFallbacksSecretQuestionIsReadThroughTheGate(t *testing.T) {
	h := newHarness(t, tuiCaps(false))
	h.p.OwnProcessStreams = true
	h.p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if _, ok := msg.(promptMsg); ok {
			panic("renderer failed")
		}
		return msg
	})}
	in := make(fifoReader)
	h.p.Stdin = in
	var secretErr atomic.Value
	done := h.run(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		// The renderer dies on this question; it is answered in plain output.
		if _, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
			Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params: []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}}}); err != nil {
			return cancelled, err
		}
		_, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepBootstrapEscrow,
			Kind: installflow.PromptSecret, Question: installflow.QuestionEscrowPassphrase,
			Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}})
		secretErr.Store(fmt.Sprint(err))
		if err != nil {
			return cancelled, err
		}
		return succeeded, endRun(rt)
	}), installflow.Request{Operation: installflow.OperationInstall})
	waitFor(t, "the plain question", h.out, screenShows(h.out, "Create it now? [y/N]: "))
	select {
	case in <- []byte("y\n"):
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read stdin")
	}
	err := h.result(done)
	if !errors.Is(err, installflow.ErrPromptUnavailable) || !strings.Contains(err.Error(), "--ui=plain") {
		t.Fatalf("the secret question after a fallback: %v (asked: %v)", err, secretErr.Load())
	}
}

// 🔴 A ONE-TIME DISCLOSURE THAT CANNOT BE SHOWN EVEN IN PLAIN OUTPUT FAILS THE RUN. The
// instance journey's screen holds the seed for its first-login screen; the screen stops
// accepting writes mid-run, and plain output (the same stdout) cannot show it either. The
// run itself succeeds, but it is not reported a success with the value shown nowhere.
func TestARevealPlainOutputCannotShowFailsTheRun(t *testing.T) {
	h := newHarness(t, instCaps())
	h.p.Caps.ErrorTTY = true
	h.p.Caps.Splash, h.p.Caps.Animate = terminal.NoArt, false // as setup's instance stage
	out := &failingWriter{}
	errs := &syncBuffer{}
	h.p.Stdout, h.p.Stderr = out, errs
	ran := make(chan struct{})
	next := make(chan struct{})
	eng := engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := rt.Events.Emit(installflow.Event{Payload: installflow.RunStarted{
			Target:   installflow.Target{Mode: "existing", Context: installflow.Text("kind-devicechain")},
			Manifest: []installflow.ManifestStep{{ID: installflow.StepBootstrapEscrow}, {ID: installflow.StepBootstrapHelm}},
		}}); err != nil {
			return installflow.Summary{}, err
		}
		rt.Reveals.Offer(seedOffer("never-shown-seed"))
		close(ran)
		<-next
		// A redraw on a screen that can no longer be written stops the program.
		if err := rt.Events.Emit(installflow.Event{Step: installflow.StepBootstrapHelm, Attempt: 1,
			Payload: installflow.StepStarted{Message: "bootstrap.helm"}}); err != nil {
			return installflow.Summary{}, err
		}
		fmt.Fprintln(rt.Console.Stdout, "a line that redraws the screen")
		<-next
		return okBootstrapSummary(), nil
	})
	h.p.Bootstrap = &BootstrapScreens{
		Setup: BootstrapSetup{Version: "v1.12.0", Target: "kind cluster devicechain",
			TargetArgs: []string{"--cluster=devicechain"},
			Escrow: EscrowSeed{DefaultPath: func(n string) string {
				return "/home/op/.devicechain/escrow/" + n + "-rootkey.escrow"
			}}},
		Handover: &Handover{Target: "kind cluster devicechain",
			Continue: "dcctl bootstrap local <instance> --cluster=devicechain"},
		Plan: func(p BootstrapPlanned) (BootstrapPlan, error) {
			return BootstrapPlan{Engine: eng, Review: Review{Rows: []ReviewRow{{Label: "Instance", Value: p.Instance.Name}}},
				Command: BootstrapCommand(p.Instance, p.Escrow, []string{"--cluster=devicechain"})}, nil
		},
	}
	done := h.run(nil, installflow.Request{Operation: installflow.OperationBootstrap})
	screen := &out.syncBuffer
	waitFor(t, "the hand-over", screen, screenShows(screen, handoverTitle))
	time.Sleep(700 * time.Millisecond)
	h.press("\r")
	waitFor(t, "the instance screen", screen, screenShows(screen, "Choose the instance to create"))
	h.press("acme")
	h.press(strings.Repeat("\t", 7) + "\r")
	waitFor(t, "the escrow screen", screen, screenShows(screen, "Root-key escrow"))
	h.press(strings.Repeat("\t", 4) + "\r")
	waitFor(t, "the instance review", screen, screenShows(screen, "Approve and create the instance"))
	time.Sleep(700 * time.Millisecond)
	h.press("\t\r")
	select {
	case <-ran:
	case <-time.After(15 * time.Second):
		t.Fatalf("the instance run never started:\n%q", screen.String())
	}
	out.failing.Store(true)
	next <- struct{}{}
	waitFor(t, "the fallback", errs, screenShows(errs, "continuing with plain output"))
	next <- struct{}{}
	if err := h.result(done); !errors.Is(err, errRevealLost) {
		t.Fatalf("a disclosure shown nowhere: the run returned %v", err)
	}
}
