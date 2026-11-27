// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// instDriver runs the program's model through the instance journey the way Bubble Tea
// would, except that it HOLDS every settle message until the test releases it: a key
// sent before settle() is exactly a key typed before the screen settled.
type instDriver struct {
	t       *testing.T
	m       model
	held    []tea.Msg
	chose   []installflow.Engine
	started int
	plans   int
	quit    bool
}

// instEngine is the run an instance plan carries in these tests.
var instEngine installflow.Engine = engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
	return succeeded, nil
})

func instScreens(plan func(BootstrapPlanned) (BootstrapPlan, error)) *BootstrapScreens {
	return &BootstrapScreens{
		Handover: &Handover{Target: "kind cluster devicechain (Standard footprint, Single)",
			Continue: "dcctl bootstrap local <instance> --cluster=devicechain"},
		Plan: plan,
	}
}

func goodPlan(BootstrapPlanned) (BootstrapPlan, error) {
	return BootstrapPlan{Engine: instEngine, Review: Review{Rows: []ReviewRow{{Label: "Instance", Value: "acme"},
		{Label: "Cluster", Value: "kind cluster devicechain (Standard footprint, Single), installed by the first stage"},
		{Label: "Profile", Value: "default"}, {Label: "Escrow", Value: "/home/op/.devicechain/escrow/acme-rootkey.escrow; " +
			"passphrase asked for when the run reaches its escrow step, twice, and never shown"}}},
		Command: "dcctl bootstrap local acme --cluster=devicechain " +
			"--escrow-passphrase-file=<file holding the escrow passphrase> --yes"}, nil
}

func newInstDriver(t *testing.T, caps terminal.Capabilities, scr *BootstrapScreens) *instDriver {
	d := &instDriver{t: t}
	if scr.Plan != nil {
		plan := scr.Plan
		scr.Plan = func(p BootstrapPlanned) (BootstrapPlan, error) { d.plans++; return plan(p) }
	}
	setup := bsSetup(caps)
	setup.TargetArgs = []string{"--cluster=devicechain"}
	scr.Setup = setup
	inst := newInstancePhase(scr, setup)
	caps.Splash = terminal.NoArt
	d.m = newModel(caps, &interrupter{cancel: func() {}, force: func() {}}, func() { d.started++ }, nil, nil,
		modelOptions{op: installflow.OperationBootstrap, inst: inst,
			choose: func(e installflow.Engine, _ installflow.PreflightRunner) { d.chose = append(d.chose, e) }})
	d.m.every = 0 // no redraw beat: every command here is run at once
	d.run(d.m.Init())
	return d
}

func (d *instDriver) run(cmd tea.Cmd) {
	for _, msg := range messages(cmd) {
		switch msg.(type) {
		case nil:
		case settleMsg:
			d.held = append(d.held, msg)
		case tea.QuitMsg:
			d.quit = true
		default:
			d.send(msg)
		}
	}
}

func (d *instDriver) send(msg tea.Msg) {
	d.t.Helper()
	next, cmd := d.m.Update(msg)
	d.m = next.(model)
	d.run(cmd)
}

func (d *instDriver) keys(names ...string) {
	d.t.Helper()
	for _, n := range names {
		d.send(bsKey(n))
	}
}

// show draws the screen and reports its frame written, as the screen writer does.
func (d *instDriver) show() {
	d.t.Helper()
	d.m.render()
	d.send(frameShownMsg{seq: math.MaxUint64})
}

// release delivers the settle messages held so far, oldest first.
func (d *instDriver) release() {
	held := d.held
	d.held = nil
	for _, m := range held {
		d.send(m)
	}
}

// settle shows the screen on now and lets its quiet period end with no key typed.
func (d *instDriver) settle() {
	d.show()
	d.release()
}

func (d *instDriver) screen() string { return bsStrip(d.m.render()) }

func (d *instDriver) stage() instStage { return d.m.inst.stage }

// toReview goes from the hand-over through the choices to the instance review, which
// is left unsettled.
func (d *instDriver) toReview() {
	d.t.Helper()
	d.settle()
	d.keys("enter") // Continue to the instance
	if d.stage() != instChoices {
		d.t.Fatalf("Continue on the hand-over: stage %d:\n%s", d.stage(), d.screen())
	}
	d.keys("a", "c", "m", "e")
	for i := 0; i < 20 && d.m.inst.flow.phase == phaseInstance; i++ {
		if d.m.inst.flow.inst.focus == fieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
	for i := 0; i < 20 && d.m.inst.flow.phase == phaseEscrow; i++ {
		if d.m.inst.flow.esc.focus == escrowFieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
	if d.stage() != instReview {
		d.t.Fatalf("the choices did not lead to the review: stage %d:\n%s", d.stage(), d.screen())
	}
}

func instCaps() terminal.Capabilities { return bsCaps(terminal.L1, 100, 40) }

// 🔴 NO KEY TYPED BEFORE A SCREEN OF THE INSTANCE JOURNEY SETTLED ACTS ON IT, and the
// approval is the instance review's Approve and nothing else: not the hand-over's
// Continue, not the choices' Continue, not a queued Tab and Enter.
func TestTheInstanceIsApprovedOnlyOnItsSettledReview(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	if d.stage() != instHandover {
		t.Fatalf("the instance journey opened on stage %d", d.stage())
	}
	// The Enter that closed the cluster stage's verdict, typed again: dropped.
	d.keys("enter", "tab", "enter")
	if d.stage() != instHandover {
		t.Fatalf("a key before the hand-over settled acted on it: stage %d", d.stage())
	}
	d.toReview()
	if len(d.chose) != 0 || d.started != 0 {
		t.Fatalf("the choices started the run: chose %d, started %d", len(d.chose), d.started)
	}
	if d.plans != 1 || !strings.Contains(d.screen(), instReviewPending) {
		t.Fatalf("plans built %d; review:\n%s", d.plans, d.screen())
	}
	// Typed ahead onto the review before it settled: Tab to Approve, Enter.
	d.keys("tab", "enter", "enter")
	if len(d.chose) != 0 || d.started != 0 || d.stage() != instReview {
		t.Fatalf("a key before the review settled approved it: chose %d, started %d", len(d.chose), d.started)
	}
	// A settle left over from an earlier visit arms nothing.
	d.send(settleMsg{gen: d.m.inst.gate.gen - 1, token: d.m.inst.gate.token})
	d.keys("tab", "enter")
	if len(d.chose) != 0 {
		t.Fatal("a stale settle armed the review")
	}
	d.settle()
	if d.m.inst.focus != reviewBack {
		t.Fatalf("the settled review is focused on %d, not Back", d.m.inst.focus)
	}
	d.keys("enter") // Back is focused: back to the choices, nothing approved
	if len(d.chose) != 0 || d.stage() != instChoices {
		t.Fatalf("Enter on Back: chose %d, stage %d", len(d.chose), d.stage())
	}
	// The next review is a new visit, which settles again before it takes a key.
	d.toReviewFromEscrow()
	d.keys("tab", "enter")
	if len(d.chose) != 0 {
		t.Fatal("the second visit was approved before it settled")
	}
	d.settle()
	d.keys("tab", "enter")
	if len(d.chose) != 1 || d.chose[0] == nil || d.started != 1 || d.stage() != instRun || d.m.prePhase {
		t.Fatalf("Approve on the settled review: chose %d, started %d, stage %d", len(d.chose), d.started, d.stage())
	}
}

func (d *instDriver) toReviewFromEscrow() {
	d.t.Helper()
	for i := 0; i < 20 && d.m.inst.flow.phase == phaseEscrow; i++ {
		if d.m.inst.flow.esc.focus == escrowFieldContinue {
			d.keys("enter")
			continue
		}
		d.keys("tab")
	}
	if d.stage() != instReview {
		d.t.Fatalf("not on the review: stage %d", d.stage())
	}
}

// A plan the command line refuses is said on the review, and Approve does nothing.
func TestAnInstancePlanThatCannotBeBuiltCannotBeApproved(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(func(BootstrapPlanned) (BootstrapPlan, error) {
		return BootstrapPlan{}, errors.New(`resolving deployment areas: unknown functional area "nope"`)
	}))
	d.toReview()
	d.settle()
	d.keys("tab", "enter")
	if len(d.chose) != 0 || d.started != 0 {
		t.Fatalf("an unbuildable plan was approved: chose %d", len(d.chose))
	}
	if v := d.screen(); !strings.Contains(v, instReviewBlocked) || !strings.Contains(v, `unknown functional area "nope"`) {
		t.Fatalf("the refusal is not said:\n%s", v)
	}
}

// Exit on the hand-over, or on the review, starts nothing: the run is never approved,
// so the program settles it as not started.
func TestExitBeforeTheInstanceApprovalStartsNothing(t *testing.T) {
	for _, where := range []string{"handover", "review", "ctrl+c"} {
		t.Run(where, func(t *testing.T) {
			d := newInstDriver(t, instCaps(), instScreens(goodPlan))
			switch where {
			case "handover":
				d.settle()
				d.keys("tab", "enter") // Continue is focused; Tab to Exit
			case "review":
				d.toReview()
				d.settle()
				d.keys("q", "enter")
			case "ctrl+c":
				d.toReview()
				d.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			}
			if len(d.chose) != 0 || !d.m.leaving {
				t.Fatalf("chose %d, leaving %v", len(d.chose), d.m.leaving)
			}
		})
	}
}

// Back from the instance screen returns to the hand-over, which settles again.
func TestBackFromTheInstanceScreenReturnsToTheHandover(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	d.settle()
	d.keys("enter")
	for i := 0; i < 20 && d.m.inst.flow.inst.focus != fieldBack; i++ {
		d.keys("tab")
	}
	d.keys("enter")
	if d.stage() != instHandover || d.m.inst.gate.armed {
		t.Fatalf("Back from the instance screen: stage %d, armed %v", d.stage(), d.m.inst.gate.armed)
	}
}

// 🔴 THE RUN'S OWN QUESTIONS ARE ASKED ON ITS OWN SCREEN. A secret goes to the passphrase
// form, never to a yes/no question, and the passphrase typed is never drawn.
func TestTheInstanceRunsSecretQuestionIsAskedOnItsForm(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	d.toReview()
	d.settle()
	d.keys("tab", "enter")
	reply := &secretReply{ch: make(chan installflow.Response, 1)}
	d.send(SecretQuestion{reply: reply, Question: installflow.Question{ID: "prompt-1", Kind: installflow.PromptSecret,
		Question: installflow.QuestionEscrowPassphrase, Params: []installflow.Param{{Name: "instance", Value: installflow.Text("acme")}}}})
	secret := canaryValue("escrow-passphrase")
	for _, r := range secret {
		d.send(bsKey(string(r)))
	}
	view := d.m.render()
	testkit.AssertNoCanary(t, "the passphrase form", []byte(view))
	d.keys("enter")
	select {
	case resp := <-reply.ch:
		if string(resp.Secret.Expose()) != secret {
			t.Fatal("the form answered something other than what was typed")
		}
		resp.Secret.Zero()
	default:
		t.Fatal("the passphrase was not handed to the run")
	}
	if d.m.prompt != nil {
		t.Fatal("the secret question was put to a yes/no prompt")
	}
}

// After a successful instance run the screens that follow it are the flow's, and Ctrl+C
// there leaves at once: the run is over and nothing remains to stop.
func TestAfterASuccessfulInstanceRunItsScreensFollow(t *testing.T) {
	scr := instScreens(goodPlan)
	scr.Facts = func(installflow.Summary) InstanceFacts { return acmeFacts() }
	d := newInstDriver(t, instCaps(), scr)
	d.toReview()
	d.settle()
	d.keys("tab", "enter")
	d.send(eventMsg{env: installflow.Envelope{Payload: okBootstrapSummary()}})
	if d.stage() != instAfter || !strings.Contains(d.screen(), "Escrow receipt") {
		t.Fatalf("after the run: stage %d:\n%s", d.stage(), d.screen())
	}
	d.send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !d.quit {
		t.Fatal("Ctrl+C after the run did not leave")
	}
}

// A failed instance run stays on the progress screen's verdict.
func TestAFailedInstanceRunStaysOnItsVerdict(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	d.toReview()
	d.settle()
	d.keys("tab", "enter")
	failed := cancelled
	failed.Outcome = installflow.RunFailed
	d.m.logs = emptyLogs{}
	d.send(eventMsg{env: installflow.Envelope{Payload: failed}})
	if d.quit || d.stage() != instRun || !strings.Contains(d.screen(), "Bootstrap failed.") {
		t.Fatalf("a failed run: quit %v, stage %d:\n%s", d.quit, d.stage(), d.screen())
	}
}

// emptyLogs is a log source with nothing in it.
type emptyLogs struct{}

func (emptyLogs) LogWindow(uint64, int, bool) installflow.LogWindow { return installflow.LogWindow{} }

// The hand-over and the instance review, at every level and size: the hand-over as it
// opens (settling) and settled, the review settled, and the review of a plan that
// could not be built.
func TestSetupScreenGoldens(t *testing.T) {
	scenes := []struct {
		name  string
		plan  func(BootstrapPlanned) (BootstrapPlan, error)
		drive func(*instDriver)
	}{
		{"handover-settling", goodPlan, func(*instDriver) {}},
		{"handover", goodPlan, func(d *instDriver) { d.settle() }},
		{"instance-review", goodPlan, func(d *instDriver) { d.toReview(); d.settle() }},
		{"instance-review-refused", func(BootstrapPlanned) (BootstrapPlan, error) {
			return BootstrapPlan{}, errors.New(`--size: compact is the cluster's; this instance follows it`)
		}, func(d *instDriver) { d.toReview(); d.settle() }},
	}
	for _, sc := range scenes {
		for _, size := range bsSizes {
			var plain string
			for _, level := range []terminal.Level{terminal.L1, terminal.L2, terminal.L3, terminal.L4} {
				d := newInstDriver(t, bsCaps(level, size[0], size[1]), instScreens(sc.plan))
				sc.drive(d)
				view := d.m.render() + "\n"
				for i, l := range strings.Split(view, "\n") {
					if w := lipgloss.Width(l); w > size[0] {
						t.Errorf("L%d %s at %dx%d: line %d is %d cells wide", level, sc.name, size[0], size[1], i+1, w)
					}
				}
				if n := strings.Count(view, "\n"); n > size[1] {
					t.Errorf("%s at %dx%d: %d lines", sc.name, size[0], size[1], n)
				}
				name := fmt.Sprintf("setup/%s/L%d-%dx%d", sc.name, level, size[0], size[1])
				testkit.AssertNoCanary(t, name, []byte(view))
				text := bsStrip(view)
				bsGolden(t, name+".txt", []byte(text))
				switch level {
				case terminal.L1:
					if strings.Contains(view, "\x1b") {
						t.Errorf("%s: an escape sequence at L1", name)
					}
					plain = text
				case terminal.L2: // ASCII borders: its words are checked by its own golden
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
				default:
					bsGolden(t, name+".ansi", []byte(bsQuote(view)))
					if text != plain {
						t.Errorf("%s: the words differ from L1's; a state is in colour alone", name)
					}
				}
			}
		}
	}
}

// 🔴 A STALLED TERMINAL CANNOT APPROVE THE INSTANCE FOR KEYS TYPED AHEAD. While the
// renderer is stalled nothing of the review has been written, so no quiet period runs and
// every key is dropped; once its frame is written, a key typed ahead and delivered then
// restarts the quiet period, so a timer that fires after it arms nothing. Only a key
// pressed after the screen was written and the keyboard went quiet acts.
func TestATypedAheadBurstNeverArmsTheInstanceReviewAfterAStall(t *testing.T) {
	d := newInstDriver(t, instCaps(), instScreens(goodPlan))
	d.toReview()
	// The stall: the review is entered but no frame of it has been written. Keys typed now
	// are dropped, and any timer that fires arms nothing.
	d.keys("tab", "enter")
	d.release()
	d.keys("tab", "enter")
	if len(d.chose) != 0 || d.m.inst.gate.armed {
		t.Fatalf("keys typed during the stall: chose %d, armed %v", len(d.chose), d.m.inst.gate.armed)
	}
	// The frame is written; the burst typed ahead is delivered after it, and then the
	// first quiet period's timer fires.
	d.show()
	first := d.held
	d.held = nil
	d.keys("tab")
	for _, m := range first {
		d.send(m)
	}
	if d.m.inst.gate.armed {
		t.Fatal("a quiet period a key interrupted armed the review")
	}
	d.keys("tab", "enter")
	if len(d.chose) != 0 {
		t.Fatal("a burst typed ahead approved the instance")
	}
	// Quiet at last: the latest period ends with no key, and only now a key counts.
	last := d.held[len(d.held)-1]
	d.held = nil
	d.send(last)
	if !d.m.inst.gate.armed || d.m.inst.focus != reviewBack {
		t.Fatalf("after a quiet period: armed %v, focus %d", d.m.inst.gate.armed, d.m.inst.focus)
	}
	d.keys("tab", "enter")
	if len(d.chose) != 1 {
		t.Fatalf("Approve after the quiet period: chose %d", len(d.chose))
	}
}
