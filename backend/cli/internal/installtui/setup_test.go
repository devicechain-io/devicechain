// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The exit-code contract of dcctl setup local, case by case. 🔴 A PARTIAL SUCCESS (the
// cluster installed, the instance not) is never 0, never 1 (which says the cluster
// failed), and never 2 (which says nothing changed).
func TestSetupExitCodes(t *testing.T) {
	for _, c := range []struct {
		cluster, instance PhaseOutcome
		want              int
	}{
		{PhaseSucceeded, PhaseSucceeded, SetupExitSucceeded},
		{PhaseNotApproved, PhaseNotReached, SetupExitNothingChanged},
		{PhaseNotReached, PhaseNotReached, SetupExitNothingChanged},
		{PhaseFailed, PhaseNotReached, SetupExitClusterFailed},
		{PhaseSucceeded, PhaseNotApproved, SetupExitInstanceNotCreated},
		{PhaseSucceeded, PhaseNotReached, SetupExitInstanceNotCreated},
		{PhaseSucceeded, PhaseFailed, SetupExitInstanceFailed},
	} {
		t.Run(fmt.Sprintf("%d-%d", c.cluster, c.instance), func(t *testing.T) {
			if got := (SetupResult{Cluster: c.cluster, Instance: c.instance}).ExitCode(); got != c.want {
				t.Fatalf("cluster %d, instance %d: exit %d, want %d", c.cluster, c.instance, got, c.want)
			}
		})
	}
	// The codes are distinct, and 0 and 1 mean what dcctl install's do.
	seen := map[int]bool{}
	for _, c := range []int{SetupExitSucceeded, SetupExitClusterFailed, SetupExitNothingChanged,
		SetupExitInstanceNotCreated, SetupExitInstanceFailed} {
		if seen[c] {
			t.Fatalf("exit status %d means two things", c)
		}
		seen[c] = true
	}
	if SetupExitSucceeded != succeeded.ExitCode() || SetupExitClusterFailed != cancelled.ExitCode() {
		t.Fatal("0 and 1 no longer mean what an install's 0 and 1 mean")
	}
}

// A stage's outcome is read from its presenter's error: through redaction's wrapping,
// a run nobody approved is told from one that failed.
func TestPhaseOfReadsTheStagesError(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		err      error
		approved bool
		want     PhaseOutcome
	}{
		{nil, true, PhaseSucceeded},
		{errNotStarted, false, PhaseNotApproved},
		{errInstanceNotStarted, false, PhaseNotApproved},
		{hub.RedactError(errInstanceNotStarted), false, PhaseNotApproved},
		{errors.New("the instance was not started"), true, PhaseFailed}, // the words alone are not the verdict
		{context.Canceled, true, PhaseFailed},
		{hub.RedactError(errors.New("helm: timed out")), true, PhaseFailed},
		// 🔴 BEFORE ITS APPROVAL A STAGE CHANGED NOTHING, whatever its error: a presenter
		// that could not take the process streams, or a terminal that failed to start.
		{errors.New("taking the process streams: too many open files"), false, PhaseNotApproved},
		{context.Canceled, false, PhaseNotApproved},
	} {
		if got := phaseOf(c.err, c.approved); got != c.want {
			t.Errorf("%v (approved %v): %d, want %d", c.err, c.approved, got, c.want)
		}
	}
}

// stageRecorder is the Setup's run seam: it records each stage it is asked to run and
// answers with the scripted error.
type stageRecorder struct {
	presenters []Presenter
	requests   []installflow.Request
	results    []error
	during     []func(Presenter)
	// unapproved marks the stages whose screens end before their approval; every other
	// stage is approved (OnApproved) unless its result is a run nobody approved.
	unapproved map[int]bool
}

func (r *stageRecorder) run(_ context.Context, p Presenter, _ installflow.Engine, req installflow.Request,
	_ installflow.PreflightRunner) error {
	i := len(r.presenters)
	r.presenters, r.requests = append(r.presenters, p), append(r.requests, req)
	var res error = errors.New("no stage was scripted here")
	if i < len(r.results) {
		res = r.results[i]
	}
	if !r.unapproved[i] && !errors.Is(res, ErrNotConfirmed) && p.OnApproved != nil {
		p.OnApproved()
	}
	if i < len(r.during) && r.during[i] != nil {
		r.during[i](p)
	}
	if i < len(r.results) {
		return r.results[i]
	}
	return errors.New("no stage was scripted here")
}

func setupUnderTest(rec *stageRecorder, out *bytes.Buffer, instance func() *BootstrapScreens) Setup {
	caps := tuiCaps(false)
	caps.Splash, caps.Animate = terminal.LargeMark, true
	return Setup{Stdout: out, Stderr: out, Caps: caps, Wizard: &Wizard{Facts: preFacts()},
		ClusterRequest: installflow.Request{Operation: installflow.OperationInstall},
		Instance:       instance,
		ContinueCommand: func(p *BootstrapPlanned) string {
			if p == nil {
				return "dcctl bootstrap local <instance> --cluster=devicechain"
			}
			return "dcctl bootstrap local " + p.Instance.Name + " --cluster=devicechain"
		},
		run: rec.run}
}

// 🔴 THE INSTANCE STAGE NEVER STARTS AFTER A CLUSTER STAGE THAT DID NOT SUCCEED: not
// after a failure, and not after the cluster was never approved. Its screens are not
// even built.
func TestTheInstanceStageNeverFollowsAClusterStageThatDidNotSucceed(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"failed", errors.New("the operator rollout timed out"), SetupExitClusterFailed},
		{"interrupted", context.Canceled, SetupExitClusterFailed},
		{"not approved", errNotStarted, SetupExitNothingChanged},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &stageRecorder{results: []error{c.err, nil}}
			var out bytes.Buffer
			s := setupUnderTest(rec, &out, func() *BootstrapScreens {
				t.Fatal("the instance screens were built after a cluster stage that did not succeed")
				return nil
			})
			res := s.Run(context.Background())
			if len(rec.presenters) != 1 {
				t.Fatalf("%d stages ran, want only the cluster's", len(rec.presenters))
			}
			if res.ExitCode() != c.want || !errors.Is(res.Err, c.err) {
				t.Fatalf("exit %d (err %v), want %d", res.ExitCode(), res.Err, c.want)
			}
			if strings.Contains(out.String(), "stays installed") {
				t.Fatalf("a cluster that did not install was said to stay installed:\n%s", out.String())
			}
		})
	}
}

// The instance stage is a presenter of its own: no wizard, no approval carried over, no
// --yes, the bootstrap operation, and no logo. The cluster stage's success says Enter
// continues.
func TestTheInstanceStageIsAFreshPresenter(t *testing.T) {
	rec := &stageRecorder{results: []error{nil, nil}}
	var out bytes.Buffer
	scr := &BootstrapScreens{Handover: &Handover{Continue: "x"}}
	res := setupUnderTest(rec, &out, func() *BootstrapScreens { return scr }).Run(context.Background())
	if res.ExitCode() != SetupExitSucceeded || len(rec.presenters) != 2 {
		t.Fatalf("exit %d after %d stages", res.ExitCode(), len(rec.presenters))
	}
	cluster, instance := rec.presenters[0], rec.presenters[1]
	if !cluster.Continues || cluster.Wizard == nil || cluster.Bootstrap != nil {
		t.Fatalf("the cluster stage: continues %v, wizard %v, bootstrap %v", cluster.Continues,
			cluster.Wizard != nil, cluster.Bootstrap != nil)
	}
	if instance.Wizard != nil || instance.Bootstrap == nil || instance.Continues {
		t.Fatalf("the instance stage: wizard %v, bootstrap %v, continues %v", instance.Wizard != nil,
			instance.Bootstrap != nil, instance.Continues)
	}
	if r := rec.requests[1]; r.Operation != installflow.OperationBootstrap || r.AssumeYes || r.DryRun {
		t.Fatalf("the instance request: %+v", r)
	}
	if instance.Caps.Splash != terminal.NoArt || instance.Caps.Animate || instance.Caps.ProgressOnly {
		t.Fatalf("the instance stage's terminal: %+v", instance.Caps)
	}
	if out.String() != "" {
		t.Fatalf("a finished setup printed a hand-over:\n%s", out.String())
	}
}

// After the cluster stage, leaving or failing the instance stage leaves the cluster
// installed, says so, and names the exact command that continues: the chosen instance's
// once its choices were settled.
func TestAnInstanceStageThatDoesNotFinishSaysTheClusterStaysInstalled(t *testing.T) {
	planned := BootstrapPlanned{Instance: InstanceChoices{Name: "acme"}}
	for _, c := range []struct {
		name   string
		err    error
		chose  bool
		want   int
		words  string
		action string
	}{
		{"left before approval", errInstanceNotStarted, false, SetupExitInstanceNotCreated,
			"no instance was created", "dcctl bootstrap local <instance> --cluster=devicechain"},
		{"left on the review", errInstanceNotStarted, true, SetupExitInstanceNotCreated,
			"no instance was created", "dcctl bootstrap local acme --cluster=devicechain"},
		{"failed", errors.New("helm: timed out"), true, SetupExitInstanceFailed,
			"creating the instance failed", "dcctl bootstrap local acme --cluster=devicechain"},
		{"interrupted", context.Canceled, true, SetupExitInstanceFailed,
			"creating the instance failed", "dcctl bootstrap local acme --cluster=devicechain"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &stageRecorder{results: []error{nil, c.err}, during: []func(Presenter){nil, func(p Presenter) {
				if c.chose {
					p.Bootstrap.Chosen(planned)
				}
			}}}
			var out bytes.Buffer
			res := setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(context.Background())
			if res.ExitCode() != c.want {
				t.Fatalf("exit %d, want %d", res.ExitCode(), c.want)
			}
			got := out.String()
			for _, w := range []string{"the cluster is installed and stays installed", c.words, "  " + c.action + "\n"} {
				if !strings.Contains(got, w) {
					t.Fatalf("missing %q in:\n%s", w, got)
				}
			}
		})
	}
}

// Interrupted between the stages: the instance stage is not begun, and the cluster is
// said to stay installed.
func TestAnInterruptBetweenTheStagesBeginsNoInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := &stageRecorder{results: []error{nil, nil}, during: []func(Presenter){func(Presenter) { cancel() }}}
	var out bytes.Buffer
	res := setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(ctx)
	if len(rec.presenters) != 1 || res.ExitCode() != SetupExitInstanceNotCreated {
		t.Fatalf("%d stages, exit %d", len(rec.presenters), res.ExitCode())
	}
	if !strings.Contains(out.String(), "stays installed") {
		t.Fatalf("the hand-over was not said:\n%s", out.String())
	}
}

// 🔴 A STAGE THAT FAILS BEFORE ITS APPROVAL CHANGED NOTHING. A presenter that cannot even
// start (it could not take the process streams) is classified by the approval it never
// had, not by its error: 2 for the cluster stage, 3 for the instance stage.
func TestAStageThatFailsBeforeItsApprovalIsNotAFailedStage(t *testing.T) {
	broken := errors.New("taking the process streams: too many open files")
	for _, c := range []struct {
		name    string
		results []error
		want    int
	}{
		{"cluster", []error{broken}, SetupExitNothingChanged},
		{"instance", []error{nil, broken}, SetupExitInstanceNotCreated},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &stageRecorder{results: c.results, unapproved: map[int]bool{len(c.results) - 1: true}}
			var out bytes.Buffer
			res := setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(context.Background())
			if res.ExitCode() != c.want || !errors.Is(res.Err, broken) {
				t.Fatalf("exit %d (%v), want %d", res.ExitCode(), res.Err, c.want)
			}
		})
	}
}

// 🔴 A PANIC IN A STAGE EXITS WITH SETUP'S STATUS, never Go's 2 (which says nothing
// changed): 1 or 4 after the stage's approval, 2 or 3 before it.
func TestAPanicInAStageExitsWithSetupsStatus(t *testing.T) {
	for _, c := range []struct {
		name       string
		at         int
		unapproved bool
		want       int
	}{
		{"cluster after approval", 0, false, SetupExitClusterFailed},
		{"cluster before approval", 0, true, SetupExitNothingChanged},
		{"instance after approval", 1, false, SetupExitInstanceFailed},
		{"instance before approval", 1, true, SetupExitInstanceNotCreated},
	} {
		t.Run(c.name, func(t *testing.T) {
			during := make([]func(Presenter), 2)
			during[c.at] = func(Presenter) { panic("engine exploded") }
			rec := &stageRecorder{results: []error{nil, nil}, during: during, unapproved: map[int]bool{c.at: c.unapproved}}
			var out bytes.Buffer
			res := setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(context.Background())
			if res.ExitCode() != c.want {
				t.Fatalf("exit %d (%v), want %d", res.ExitCode(), res.Err, c.want)
			}
			if !strings.Contains(out.String(), "panicked: engine exploded") {
				t.Fatalf("the panic was not reported:\n%s", out.String())
			}
			if c.at == 1 && !strings.Contains(out.String(), "stays installed") {
				t.Fatalf("an instance stage that panicked does not say the cluster stays installed:\n%s", out.String())
			}
		})
	}
}

// 🔴 CTRL+C AFTER THE CLUSTER STAGE SUCCEEDED OPENS NO HAND-OVER. The interrupt was the
// cluster presenter's to see (a raw-mode keypress reaches no other context); it reports
// it, and setup stops there: exit 3, with the command that continues.
func TestCtrlCAfterTheClusterSucceededOpensNoHandover(t *testing.T) {
	rec := &stageRecorder{results: []error{nil, nil}, during: []func(Presenter){func(p Presenter) { p.OnInterrupt() }}}
	var out bytes.Buffer
	res := setupUnderTest(rec, &out, func() *BootstrapScreens {
		t.Fatal("the instance screens were built after an interrupt")
		return nil
	}).Run(context.Background())
	if len(rec.presenters) != 1 || res.ExitCode() != SetupExitInstanceNotCreated {
		t.Fatalf("%d stages, exit %d", len(rec.presenters), res.ExitCode())
	}
	if got := out.String(); !strings.Contains(got, "stays installed") ||
		!strings.Contains(got, "dcctl bootstrap local <instance> --cluster=devicechain") {
		t.Fatalf("the hand-over was not said:\n%s", got)
	}
}

// 🔴 A SECOND CTRL+C EXITS WITH SETUP'S STATUS FOR WHERE IT CAME: 2 before the cluster's
// approval, 1 after it, 3 before the instance's approval, 4 after it.
func TestEachStagesForcedExitIsSetups(t *testing.T) {
	rec := &stageRecorder{results: []error{nil, nil}}
	var out bytes.Buffer
	setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(context.Background())
	if len(rec.presenters) != 2 {
		t.Fatalf("%d stages", len(rec.presenters))
	}
	for i, want := range [][2]int{{SetupExitNothingChanged, SetupExitClusterFailed},
		{SetupExitInstanceNotCreated, SetupExitInstanceFailed}} {
		f := rec.presenters[i].ForcedExitCode
		if f == nil || f(false) != want[0] || f(true) != want[1] {
			t.Fatalf("stage %d: forced exit is not setup's", i)
		}
	}
}

// Through the real program: the second interrupt exits with the code the caller gives
// for the approval as it stands. Install's presenter has none, and exits 1 as always.
func TestTheForcedExitUsesTheCallersCode(t *testing.T) {
	stubborn := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		<-ctx.Done()
		select {} // stopping gracefully, for ever
	})
	for _, c := range []struct {
		name string
		code func(bool) int
		want int
	}{
		{"install, unchanged", nil, 1},
		{"a caller's code, no approval given", func(a bool) int {
			if a {
				return 44
			}
			return 33
		}, 33},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, tuiCaps(true))
			h.p.ForcedExitCode = c.code
			done := h.run(stubborn, installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
			waitFor(t, "the run", h.out, screenShows(h.out, "Resolve target"))
			h.press("\x03")
			waitFor(t, "the stopping notice", h.out, screenShows(h.out, "Interrupt received."))
			h.press("\x03")
			select {
			case code := <-h.exits:
				if code != c.want {
					t.Fatalf("forced exit %d, want %d", code, c.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no forced exit")
			}
			_ = h.result(done)
		})
	}
	// After an approval, the code is the caller's for an approved stage: the PTY test
	// TestSetupPTYADoubleCtrlCAfterTheInstanceApprovalExitsFour drives that end to end.
}

// 🔴 A PANIC IN THE SCREENS IS NOT A SUCCESS UNDER SETUP. Bubble Tea recovers a panic in
// the model (a success screen's Facts, say) and returns ErrProgramPanic; the run itself
// had succeeded. Under setup (ProgramErrorFails) that is the stage's failure, since the
// result was never shown; install keeps its own behaviour, the run's result.
func TestAPanicInTheScreensAfterTheRunFailsTheStageUnderSetup(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(fmt.Sprintf("ProgramErrorFails=%v", fails), func(t *testing.T) {
			h := newHarness(t, tuiCaps(true))
			h.p.ProgramErrorFails = fails
			h.p.options = []tea.ProgramOption{tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
				if e, ok := msg.(eventMsg); ok {
					if _, ok := e.env.Payload.(installflow.Summary); ok {
						panic("the success screen exploded")
					}
				}
				return msg
			})}
			done := h.run(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
				if err := startRun(rt); err != nil {
					return installflow.Summary{}, err
				}
				return succeeded, endRun(rt)
			}), installflow.Request{Operation: installflow.OperationInstall, AssumeYes: true})
			err := h.result(done)
			switch {
			case fails && !errors.Is(err, tea.ErrProgramPanic):
				t.Fatalf("under setup a panic in the screens returned %v", err)
			case !fails && err != nil:
				t.Fatalf("install's behaviour changed: %v", err)
			}
		})
	}
}

// Setup's presenters fail a stage on a program error.
func TestSetupsPresentersFailOnAProgramError(t *testing.T) {
	rec := &stageRecorder{results: []error{nil, nil}}
	var out bytes.Buffer
	setupUnderTest(rec, &out, func() *BootstrapScreens { return &BootstrapScreens{} }).Run(context.Background())
	for i, p := range rec.presenters {
		if !p.ProgramErrorFails {
			t.Fatalf("stage %d does not fail on a program error", i)
		}
	}
}
