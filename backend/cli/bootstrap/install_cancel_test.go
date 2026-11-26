// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

// cancelHarness is one engine run through a real hub on a manual clock, gated by an
// Admission, whose stages a test drives by hand and holds at one point of its choosing.
type cancelHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelCauseFunc
	clock  *testkit.ManualClock
	sink   *testkit.CaptureSink
	hub    *installflow.Hub
	adm    *installflow.Admission
	done   chan struct{}
	sum    installflow.Summary
	err    error
}

// startCancelRun runs e. Engine runs that need a responder for a prompt pass one.
func startCancelRun(t *testing.T, adm *installflow.Admission, e *InstallEngine, pf installflow.PreflightRunner,
	responder installflow.Responder) *cancelHarness {
	t.Helper()
	h := &cancelHarness{t: t, clock: testkit.NewManualClock(testkit.Epoch), sink: &testkit.CaptureSink{},
		adm: adm, done: make(chan struct{})}
	h.ctx, h.cancel = context.WithCancelCause(context.Background())
	t.Cleanup(func() { h.cancel(nil) })
	hub, err := installflow.NewHub(installflow.Options{Sink: h.sink, RunID: "cancel-test", Clock: h.clock})
	if err != nil {
		t.Fatal(err)
	}
	h.hub = hub
	var out, errOut lockedBuffer
	go func() {
		defer close(h.done)
		h.sum, h.err = hub.Run(h.ctx, e, installflow.Request{Operation: installflow.OperationInstall,
			DryRun: e.Options.DryRun, AssumeYes: e.Options.AssumeYes},
			installflow.Runtime{
				Prompts:   installflow.NewBroker(hub, responder, e.Options.AssumeYes),
				Preflight: pf,
				Console:   installflow.Console{Stdout: &out, Stderr: &errOut},
				Admission: adm,
			})
		<-hub.Done()
	}()
	return h
}

// waitFor polls the delivered stream until it holds an event of kind.
func (h *cancelHarness) waitFor(kind installflow.Kind) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.hub.Flush()
		for _, k := range h.sink.Kinds() {
			if k == kind {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatalf("no %s within 10s; stream so far: %v", kind, h.sink.Kinds())
}

// finished waits for the run and returns its stream, checked against the protocol.
func (h *cancelHarness) finished() []installflow.Envelope {
	h.t.Helper()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		h.t.Fatal("the run never returned")
	}
	envs := h.sink.Envelopes()
	if err := testkit.VerifyStream(envs); err != nil {
		h.t.Errorf("the stream breaks the protocol: %v", err)
	}
	return envs
}

// returned reports whether the engine has returned.
func (h *cancelHarness) returned() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// cancellings is every run.cancelling in envs.
func cancellings(envs []installflow.Envelope) []installflow.RunCancelling {
	var out []installflow.RunCancelling
	for _, e := range envs {
		if c, ok := e.Payload.(installflow.RunCancelling); ok {
			out = append(out, c)
		}
	}
	return out
}

// lastFailure is the last step.failed in envs, and its index.
func lastFailure(envs []installflow.Envelope) (int, installflow.Envelope, installflow.StepFailed) {
	for i := len(envs) - 1; i >= 0; i-- {
		if f, ok := envs[i].Payload.(installflow.StepFailed); ok {
			return i, envs[i], f
		}
	}
	return -1, installflow.Envelope{}, installflow.StepFailed{}
}

func indexOf(envs []installflow.Envelope, kind installflow.Kind) int {
	for i, e := range envs {
		if e.Kind == kind {
			return i
		}
	}
	return -1
}

// clusterStages are the stages Install itself enters, in order.
var clusterStages = []installflow.StepID{
	installflow.StepInstallCluster, installflow.StepInstallClaim, installflow.StepInstallInspect,
	installflow.StepInstallCredentials, installflow.StepInstallPrepare, installflow.StepInstallWriteIntent,
	installflow.StepInstallOperator, installflow.StepInstallPrerequisites, installflow.StepInstallDatabaseLogin,
	installflow.StepInstallRecord, installflow.StepInstallRDBReady, installflow.StepInstallSummary,
}

// heldAt is a Cluster stand-in that walks Install's stages in order to stop, leaving
// behind what each stage it passes leaves (the claim held, the record written, the
// claim handed back), runs mark in stop, and then holds there as a stage's work does:
// it notices the cancel, and goes on settling until release is closed.
func heldAt(stop installflow.StepID, mark func(*installRun), reached, release chan struct{}) func(context.Context, Provider, InstallOptions) error {
	return func(ctx context.Context, _ Provider, _ InstallOptions) error {
		run := runFrom(ctx)
		for _, id := range clusterStages {
			run.enter(id)
			switch id {
			case installflow.StepInstallClaim:
				run.claimHeld()
			case installflow.StepInstallWriteIntent:
				run.recordIs(installflow.RecordApplying)
			case installflow.StepInstallRecord:
				if stop != id {
					run.recordIs(installflow.RecordInstalled)
					run.claimReleased()
				}
			}
			if id == stop {
				break
			}
		}
		if mark != nil {
			mark(run)
		}
		close(reached)
		<-ctx.Done()
		<-release // still settling: the work has not returned yet
		return fmt.Errorf("%s: %w", stop, ctx.Err())
	}
}

// 🔴 THE STOPPING STATE, IN EVERY STAGE. A cancel is announced at once — one
// run.cancelling, naming the stage, however many times it is requested — and then the
// run waits: no step ends and no summary is sent, the engine does not return, and a
// re-run is refused, for as long as the stage's work is still settling. Only once it
// has returned does the stage close as cancelled, with the effect it actually had, and
// the summary follow.
func TestInstallEngineCancelInEveryStageWaitsForTheStageToSettle(t *testing.T) {
	type tc struct {
		stage  installflow.StepID
		mark   func(*installRun)
		effect installflow.EffectCertainty
		claim  installflow.ClaimState
		record installflow.RecordState
		run    installflow.EffectCertainty
	}
	may, none, applied := installflow.EffectMayHaveChanged, installflow.EffectNoneObserved, installflow.EffectApplied
	held, notClaimed, released := installflow.ClaimHeld, installflow.ClaimNotClaimed, installflow.ClaimReleased
	cases := []tc{
		{stage: installflow.StepInstallCluster, effect: none, claim: notClaimed, record: installflow.RecordNotRead, run: none},
		{stage: installflow.StepInstallCluster, mark: (*installRun).mayChange, effect: may, claim: notClaimed,
			record: installflow.RecordNotRead, run: may}, // creating a kind cluster
		{stage: installflow.StepInstallClaim, effect: may, claim: held, record: installflow.RecordNotRead, run: may},
		{stage: installflow.StepInstallInspect, effect: none, claim: held, record: installflow.RecordNotRead, run: may},
		{stage: installflow.StepInstallCredentials, effect: may, claim: held, record: installflow.RecordNotRead, run: may},
		{stage: installflow.StepInstallPrepare, effect: may, claim: held, record: installflow.RecordNotRead, run: may},
		{stage: installflow.StepInstallWriteIntent, effect: may, claim: held, record: installflow.RecordApplying, run: may},
		{stage: installflow.StepInstallOperator, effect: may, claim: held, record: installflow.RecordApplying, run: may},
		{stage: installflow.StepInstallOperator, mark: (*installRun).changeApplied, effect: applied, claim: held,
			record: installflow.RecordApplying, run: applied}, // applied, waiting for the rollout
		{stage: installflow.StepInstallPrerequisites, effect: none, claim: held, record: installflow.RecordApplying, run: may},
		{stage: installflow.StepInstallPrerequisites, mark: (*installRun).mayChange, effect: may, claim: held,
			record: installflow.RecordApplying, run: may}, // an OpenTofu apply has started
		{stage: installflow.StepInstallDatabaseLogin, effect: may, claim: held, record: installflow.RecordApplying, run: may},
		{stage: installflow.StepInstallRecord, effect: may, claim: held, record: installflow.RecordApplying, run: may},
		{stage: installflow.StepInstallRDBReady, effect: none, claim: released, record: installflow.RecordInstalled, run: applied},
		{stage: installflow.StepInstallSummary, effect: none, claim: released, record: installflow.RecordInstalled, run: applied},
	}
	for _, c := range cases {
		name := string(c.stage)
		if c.mark != nil {
			name += "/marked"
		}
		t.Run(name, func(t *testing.T) {
			var adm installflow.Admission
			reached, release := make(chan struct{}), make(chan struct{})
			h := startCancelRun(t, &adm, &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
				Cluster: heldAt(c.stage, c.mark, reached, release)}, nil, nil)
			<-reached
			h.clock.Advance(42 * time.Second)

			// One Ctrl+C arrives twice — the keypress and the signal — and a third is the
			// operator pressing again. It is one cancel, and the first reason stands.
			h.cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
			h.cancel(installflow.CancelRequest{Reason: installflow.CancelSignal})
			h.cancel(nil)
			h.waitFor(installflow.KindRunCancelling)

			// Stopping, not stopped: nothing ends while the work is still settling.
			h.clock.Advance(time.Minute)
			h.hub.Flush()
			for _, k := range h.sink.Kinds() {
				if k == installflow.KindRunSummary || k == installflow.KindStepFailed {
					t.Fatalf("the run reported %s while the stage's work was still settling: %v", k, h.sink.Kinds())
				}
			}
			if h.returned() {
				t.Fatal("the engine returned while the stage's work was still settling")
			}
			if _, err := adm.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
				t.Fatalf("a re-run was admitted while the run was still stopping: %v", err)
			}

			close(release)
			envs := h.finished()

			cs := cancellings(envs)
			if len(cs) != 1 {
				t.Fatalf("%d run.cancelling events, want exactly 1", len(cs))
			}
			if cs[0].Reason != installflow.CancelUser || cs[0].ActiveStep != c.stage || cs[0].WaitingForTool {
				t.Errorf("run.cancelling = %+v", cs[0])
			}
			fi, fe, f := lastFailure(envs)
			if fi < indexOf(envs, installflow.KindRunCancelling) {
				t.Error("the stage ended before the stop was announced")
			}
			if fe.StepID != c.stage || !f.Cancelled || f.Code != installflow.ErrorRunCancelled ||
				f.Remedy != installflow.RemedyRerunSameCommand || f.Effect != c.effect {
				t.Errorf("the stopped stage closed as %s %+v, want %s cancelled with effect %s", fe.StepID, f, c.stage, c.effect)
			}
			if f.DurationMS != (time.Minute + 42*time.Second).Milliseconds() {
				t.Errorf("the stage's duration = %dms; it ran for 102s on the run's clock", f.DurationMS)
			}
			s := h.sum
			if s.Outcome != installflow.RunCancelled || s.FailedStep != c.stage || s.Claim != c.claim ||
				s.InstallRecord != c.record || s.PartialState != c.run {
				t.Errorf("summary = %+v, want cancelled at %s, claim %s, record %s, partial %s",
					s, c.stage, c.claim, c.record, c.run)
			}
			if s.ExitCode() != 1 {
				t.Errorf("a cancelled run exits %d", s.ExitCode())
			}
			if !errors.Is(h.err, context.Canceled) {
				t.Errorf("err = %v, want the stage's own cancellation", h.err)
			}
			if _, err := adm.Admit(); err != nil {
				t.Errorf("a settled run still refuses a re-run: %v", err)
			}
		})
	}
}

// The preflight is a stage too, and a read-only one: stopped there, it changed nothing.
func TestInstallEngineCancelDuringThePreflightChangedNothing(t *testing.T) {
	var adm installflow.Admission
	reached, release := make(chan struct{}), make(chan struct{})
	pf := installflow.PreflightFunc(func(ctx context.Context, _ func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		close(reached)
		<-ctx.Done()
		<-release
		return installflow.PreflightResult{}, ctx.Err()
	})
	h := startCancelRun(t, &adm, &InstallEngine{Provider: &fakeProvider{name: "local"}, Cluster: noCluster(t)}, pf, nil)
	<-reached
	h.cancel(installflow.CancelRequest{Reason: installflow.CancelSignal})
	h.waitFor(installflow.KindRunCancelling)
	if h.returned() {
		t.Fatal("the engine returned while the preflight was still settling")
	}
	close(release)
	envs := h.finished()
	_, fe, f := lastFailure(envs)
	if fe.StepID != installflow.StepInstallPreflight || !f.Cancelled || f.Effect != installflow.EffectNoneObserved {
		t.Errorf("failure = %s %+v", fe.StepID, f)
	}
	if h.sum.Outcome != installflow.RunCancelled || h.sum.PartialState != installflow.EffectNoneObserved ||
		h.sum.Claim != installflow.ClaimNotClaimed {
		t.Errorf("summary = %+v", h.sum)
	}
}

// A run cancelled before it began announces the stop before its first stage, and
// names none.
func TestInstallEngineCancelledBeforeItBeganSaysSoFirst(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
	pf := installflow.PreflightFunc(func(ctx context.Context, _ func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		return installflow.PreflightResult{}, ctx.Err()
	})
	r := runInstallEngine(t, ctx, &InstallEngine{Provider: &fakeProvider{name: "local"}, Cluster: noCluster(t)}, pf)
	if len(r.envs) < 2 || r.envs[1].Kind != installflow.KindRunCancelling {
		t.Fatalf("stream = %v, want run.cancelling straight after run.started", kindsOf(r.envs))
	}
	if c := r.envs[1].Payload.(installflow.RunCancelling); c.ActiveStep != "" || c.Reason != installflow.CancelUser {
		t.Errorf("run.cancelling = %+v", c)
	}
	if len(cancellings(r.envs)) != 1 || r.sum.Outcome != installflow.RunCancelled {
		t.Errorf("summary %+v, stream %v", r.sum, kindsOf(r.envs))
	}
}

func kindsOf(envs []installflow.Envelope) []installflow.Kind {
	var out []installflow.Kind
	for _, e := range envs {
		out = append(out, e.Kind)
	}
	return out
}

// 🔴 A CANCEL DURING A PROMPT. The real Install asks whether to create the missing kind
// cluster; the interrupt lands while the question is open. The prompt resolves as
// cancelled, the stage closes cancelled having changed nothing, and the error the
// command prints is the one it has always printed for an unanswered question.
func TestInstallEngineCancelDuringThePrompt(t *testing.T) {
	deadKubeconfig(t)
	var adm installflow.Admission
	responder := testkit.NewScriptedResponder(testkit.Reply{WaitForCancel: true})
	h := startCancelRun(t, &adm, &InstallEngine{Provider: localProvider{}, SkipPreflight: true,
		Options: InstallOptions{Options: Options{Cluster: "lab", ImageRegistry: "example.invalid/dc", ImageVersion: "v1"}}},
		nil, responder)
	select {
	case <-responder.Asked():
	case <-time.After(10 * time.Second):
		t.Fatal("the question was never asked")
	}
	h.cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
	h.cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
	envs := h.finished()
	if h.err == nil || !strings.Contains(h.err.Error(), `no kind cluster "lab", and it was not created`) {
		t.Fatalf("err = %v, want the refusal the command has always printed", h.err)
	}
	var disposition installflow.PromptDisposition
	for _, e := range envs {
		if p, ok := e.Payload.(installflow.PromptResolved); ok {
			disposition = p.Disposition
		}
	}
	if disposition != installflow.PromptCancelled {
		t.Errorf("the prompt resolved %q, want cancelled", disposition)
	}
	if cs := cancellings(envs); len(cs) != 1 || cs[0].ActiveStep != installflow.StepInstallCluster {
		t.Errorf("run.cancelling = %+v", cs)
	}
	_, fe, f := lastFailure(envs)
	if fe.StepID != installflow.StepInstallCluster || !f.Cancelled || f.Effect != installflow.EffectNoneObserved {
		t.Errorf("failure = %s %+v", fe.StepID, f)
	}
	if h.sum.Outcome != installflow.RunCancelled || h.sum.PartialState != installflow.EffectNoneObserved ||
		h.sum.Claim != installflow.ClaimNotClaimed {
		t.Errorf("summary = %+v", h.sum)
	}
}

// heldInTool is a Cluster stand-in that walks to the cluster prerequisites and runs
// one OpenTofu-like command there through runTool, on the production budgets: the
// apply marks the stage changing, as tofuExec.Apply does, and tool is what the
// command does once started.
func heldInTool(tool func(ctx context.Context) error, toolErr *error) func(context.Context, Provider, InstallOptions) error {
	return heldInToolMarked(true, tool, toolErr)
}

// heldInToolMarked is heldInTool with the apply's changing mark optional: an OpenTofu
// init, say, writes nothing to the cluster and marks nothing.
func heldInToolMarked(changing bool, tool func(ctx context.Context) error, toolErr *error) func(context.Context, Provider, InstallOptions) error {
	return func(ctx context.Context, _ Provider, _ InstallOptions) error {
		run := runFrom(ctx)
		for _, id := range clusterStages[:8] {
			run.enter(id)
			if id == installflow.StepInstallClaim {
				run.claimHeld()
			}
		}
		if changing {
			run.mayChange()
		}
		err := runTool(ctx, tofuAbandonBudget, tofuGracefulStopBudget, func() error { return tool(ctx) })
		*toolErr = err
		if run != nil {
			run.claimReleased() // Install's deferred release
		}
		return err
	}
}

// 🔴 CANCEL DURING AN OPENTOFU APPLY THAT STOPS GRACEFULLY, ON THE PRODUCTION BUDGETS
// AND A FAKE CLOCK. The stop says it is waiting for a tool, and for how long that tool
// was given; the run then waits the whole graceful stop out — four minutes of tofu
// writing its state — and only then ends the stage, as cancelled, never as having
// changed nothing.
func TestInstallEngineCancelDuringATofuApplyWaitsForItsGracefulStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var adm installflow.Admission
		var toolErr error
		stopped := errors.New("tofu stopped and wrote its state")
		started := make(chan struct{})
		sink := &testkit.CaptureSink{}
		hub, err := installflow.NewHub(installflow.Options{Sink: sink, RunID: "tofu-cancel"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		e := &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
			Cluster: heldInTool(func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				time.Sleep(4 * time.Minute) // the graceful stop: finishing in-flight work, writing state
				return stopped
			}, &toolErr)}
		var sum installflow.Summary
		done := make(chan struct{})
		go func() {
			defer close(done)
			sum, _ = hub.Run(ctx, e, installflow.Request{Operation: installflow.OperationInstall},
				installflow.Runtime{Prompts: installflow.NewBroker(hub, nil, false), Admission: &adm,
					Console: installflow.Console{Stdout: &lockedBuffer{}, Stderr: &lockedBuffer{}}})
			<-hub.Done()
		}()
		<-started
		begin := time.Now()
		cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
		synctest.Wait()
		hub.Flush()
		cs := cancellings(sink.Envelopes())
		if len(cs) != 1 || !cs[0].WaitingForTool || cs[0].StopBudgetMS != tofuGracefulStopBudget.Milliseconds() ||
			cs[0].ActiveStep != installflow.StepInstallPrerequisites {
			t.Fatalf("run.cancelling = %+v, want one, waiting for the tool with its 20-minute stop", cs)
		}
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		if _, err := adm.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
			t.Fatalf("a re-run was admitted during the apply's graceful stop: %v", err)
		}
		select {
		case <-done:
			t.Fatal("the run finished during the apply's graceful stop")
		default:
		}
		<-done
		if waited := time.Since(begin); waited != 4*time.Minute {
			t.Errorf("the run settled after %s, want the apply's own 4m", waited)
		}
		if !errors.Is(toolErr, stopped) {
			t.Errorf("the apply's own result did not come back: %v", toolErr)
		}
		envs := sink.Envelopes()
		if err := testkit.VerifyStream(envs); err != nil {
			t.Errorf("the stream breaks the protocol: %v", err)
		}
		_, fe, f := lastFailure(envs)
		if fe.StepID != installflow.StepInstallPrerequisites || !f.Cancelled ||
			f.Code != installflow.ErrorRunCancelled || f.Effect != installflow.EffectMayHaveChanged {
			t.Errorf("failure = %s %+v, want the prerequisites cancelled, may_have_changed", fe.StepID, f)
		}
		if sum.Outcome != installflow.RunCancelled || sum.PartialState != installflow.EffectMayHaveChanged {
			t.Errorf("summary = %+v", sum)
		}
		if _, err := adm.Admit(); err != nil {
			t.Errorf("not admitted once settled: %v", err)
		}
	})
}

// 🔴 AN ABANDONED APPLY, ON THE PRODUCTION BUDGETS AND A FAKE CLOCK. The interrupted
// command never comes back; after exactly tofuAbandonBudget the run gives up on it and
// says so — run.tool-abandoned, may_have_changed, re-run the same command — and the
// summary follows. But the command is still out there, so the run is NOT settled: a
// re-run is refused until the parked command really exits.
func TestInstallEngineAnAbandonedApplyHoldsTheReRunOutUntilItExits(t *testing.T) {
	// Unmarked: a command that marked nothing (an init) is still never none_observed
	// once it has been given up on — it was not seen to finish.
	for _, changing := range []bool{true, false} {
		t.Run(fmt.Sprintf("marked=%v", changing), func(t *testing.T) { abandonedApply(t, changing) })
	}
}

func abandonedApply(t *testing.T, changing bool) {
	synctest.Test(t, func(t *testing.T) {
		var adm installflow.Admission
		var toolErr error
		started, parked := make(chan struct{}), make(chan struct{})
		sink := &testkit.CaptureSink{}
		hub, err := installflow.NewHub(installflow.Options{Sink: sink, RunID: "tofu-abandon"})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancelCause(t.Context())
		e := &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
			Cluster: heldInToolMarked(changing, func(ctx context.Context) error {
				close(started)
				<-parked // a provider plugin holding the pipe: nothing ends this from here
				return nil
			}, &toolErr)}
		var sum installflow.Summary
		var runErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			sum, runErr = hub.Run(ctx, e, installflow.Request{Operation: installflow.OperationInstall},
				installflow.Runtime{Prompts: installflow.NewBroker(hub, nil, false), Admission: &adm,
					Console: installflow.Console{Stdout: &lockedBuffer{}, Stderr: &lockedBuffer{}}})
			<-hub.Done()
		}()
		<-started
		begin := time.Now()
		cancel(nil)
		<-done
		if waited := time.Since(begin); waited != tofuAbandonBudget {
			t.Errorf("the run gave up after %s, want exactly the abandon budget %s", waited, tofuAbandonBudget)
		}
		if !errors.Is(toolErr, errTofuAbandoned) || !errors.Is(runErr, errTofuAbandoned) {
			t.Fatalf("tool %v, run %v: want the abandon error, whose words the plain output prints", toolErr, runErr)
		}
		envs := sink.Envelopes()
		if err := testkit.VerifyStream(envs); err != nil {
			t.Errorf("the stream breaks the protocol: %v", err)
		}
		cs := cancellings(envs)
		if len(cs) != 1 || cs[0].Reason != installflow.CancelSignal || !cs[0].WaitingForTool {
			t.Errorf("run.cancelling = %+v", cs)
		}
		_, fe, f := lastFailure(envs)
		if fe.StepID != installflow.StepInstallPrerequisites || !f.Cancelled || f.Code != installflow.ErrorToolAbandoned ||
			f.Remedy != installflow.RemedyRerunSameCommand || f.Effect != installflow.EffectMayHaveChanged {
			t.Errorf("failure = %s %+v, want the prerequisites abandoned, may_have_changed, re-run", fe.StepID, f)
		}
		if sum.Outcome != installflow.RunCancelled || sum.PartialState != installflow.EffectMayHaveChanged ||
			sum.Claim != installflow.ClaimReleased {
			t.Errorf("summary = %+v", sum)
		}

		// Reported, but not settled: the command is still running.
		select {
		case <-adm.Settled():
			t.Fatal("settled while the abandoned apply is still running")
		default:
		}
		if _, err := adm.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
			t.Fatalf("a re-run was admitted on top of the abandoned apply: %v", err)
		}
		close(parked)
		synctest.Wait()
		if _, err := adm.Admit(); err != nil {
			t.Errorf("not admitted once the abandoned apply exited: %v", err)
		}
	})
}

// The words for an abandoned apply say what the plain error says: partly applied, not
// untouched; nothing rolled back; re-run the same command.
func TestTheAbandonedApplyWordsMatchThePlainError(t *testing.T) {
	words := installflow.ErrorWords(installflow.ErrorToolAbandoned)
	for _, want := range []string{"stopped waiting for the interrupted OpenTofu command", "partly applied, not untouched",
		"Nothing is being rolled back", "Re-run the same command"} {
		if !strings.Contains(words, want) {
			t.Errorf("the abandoned-apply words do not say %q:\n%s", want, words)
		}
	}
	for _, want := range []string{"stopped waiting for the interrupted OpenTofu command", "PARTIALLY APPLIED", "Re-run the same command"} {
		if !strings.Contains(errTofuAbandoned.Error(), want) {
			t.Errorf("the plain abandon error no longer says %q", want)
		}
	}
	if installflow.EffectWords(installflow.EffectMayHaveChanged) == installflow.EffectWords(installflow.EffectNoneObserved) ||
		installflow.EffectWords("unknown") != installflow.EffectWords(installflow.EffectMayHaveChanged) {
		t.Error("only none_observed may read as nothing changed")
	}
}

// fakeApplyTofu is a POSIX tofu whose apply does what body says.
func fakeApplyTofu(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX fake tofu")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  version) echo '{\"terraform_version\":\"1.8.0\",\"platform\":\"linux_amd64\",\"provider_selections\":{}}' ;;\n" +
		"  apply)   " + body + " ;;\n" +
		"  *)       echo \"unexpected subcommand $1\" >&2; exit 1 ;;\n" +
		"esac\n"
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// applyingPrereqs is a Cluster stand-in that walks to the cluster prerequisites and
// runs a real tofuExec.Apply of bin there, then — when after is set — goes on to
// whatever after does in the same stage.
func applyingPrereqs(bin string, abandon time.Duration, after func(context.Context) error) func(context.Context, Provider, InstallOptions) error {
	return func(ctx context.Context, _ Provider, _ InstallOptions) error {
		run := runFrom(ctx)
		for _, id := range clusterStages[:8] {
			run.enter(id)
		}
		tf, err := newTofuExec(filepath.Dir(bin), bin, "")
		if err != nil {
			return err
		}
		tf.abandonBudget = abandon
		if err := tf.Apply(ctx); err != nil {
			return err
		}
		if after != nil {
			return after(ctx)
		}
		return nil
	}
}

// 🔴 THE EFFECT OF A STAGE IS WHAT ITS OPENTOFU APPLY DID, through the real
// tofuExec.Apply. The prerequisites stage starts having changed nothing; an apply
// that started and failed may have changed the cluster; one that returned success
// certainly did; and one abandoned mid-flight holds a re-run out until it exits.
func TestInstallEngineTheEffectOfARealTofuApply(t *testing.T) {
	testKube(t)

	t.Run("failed apply may have changed the cluster", func(t *testing.T) {
		bin := fakeApplyTofu(t, "echo 'Error: provider refused' >&2; exit 1")
		h := startCancelRun(t, &installflow.Admission{}, &InstallEngine{Provider: &fakeProvider{name: "local"},
			SkipPreflight: true, Cluster: applyingPrereqs(bin, time.Minute, nil)}, nil, nil)
		envs := h.finished()
		_, fe, f := lastFailure(envs)
		if fe.StepID != installflow.StepInstallPrerequisites || f.Cancelled ||
			f.Effect != installflow.EffectMayHaveChanged {
			t.Errorf("failure = %s %+v, want the prerequisites failed with may_have_changed: an apply had started", fe.StepID, f)
		}
	})

	t.Run("cancelled after a successful apply has applied", func(t *testing.T) {
		bin := fakeApplyTofu(t, "exit 0")
		reached := make(chan struct{})
		h := startCancelRun(t, &installflow.Admission{}, &InstallEngine{Provider: &fakeProvider{name: "local"},
			SkipPreflight: true, Cluster: applyingPrereqs(bin, time.Minute, func(ctx context.Context) error {
				close(reached) // the rollout confirmation, say
				<-ctx.Done()
				return ctx.Err()
			})}, nil, nil)
		<-reached
		h.cancel(installflow.CancelRequest{Reason: installflow.CancelUser})
		envs := h.finished()
		_, fe, f := lastFailure(envs)
		if fe.StepID != installflow.StepInstallPrerequisites || !f.Cancelled || f.Effect != installflow.EffectApplied {
			t.Errorf("failure = %s %+v, want the prerequisites cancelled with applied", fe.StepID, f)
		}
		if h.sum.PartialState != installflow.EffectApplied {
			t.Errorf("summary = %+v", h.sum)
		}
	})

	t.Run("abandoned apply holds the re-run out until it exits", func(t *testing.T) {
		dir := t.TempDir()
		ready := filepath.Join(dir, "spawned")
		// `sleep 2 &` inherits the stdout pipe and outlives tofu: terraform-exec cannot
		// return until it ends, two seconds after the apply began.
		bin := fakeApplyTofu(t, "sleep 2 & : > "+ready+"; exit 0")
		var adm installflow.Admission
		h := startCancelRun(t, &adm, &InstallEngine{Provider: &fakeProvider{name: "local"},
			SkipPreflight: true, Cluster: applyingPrereqs(bin, 50*time.Millisecond, nil)}, nil, nil)
		deadline := time.Now().Add(15 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the fake tofu never ran its apply")
			}
			time.Sleep(5 * time.Millisecond)
		}
		h.cancel(nil)
		envs := h.finished()
		if !errors.Is(h.err, errTofuAbandoned) {
			t.Fatalf("err = %v, want the abandon error", h.err)
		}
		_, fe, f := lastFailure(envs)
		if fe.StepID != installflow.StepInstallPrerequisites || f.Code != installflow.ErrorToolAbandoned ||
			f.Effect != installflow.EffectMayHaveChanged {
			t.Errorf("failure = %s %+v", fe.StepID, f)
		}
		if _, err := adm.Admit(); !errors.Is(err, installflow.ErrNotSettled) {
			t.Fatalf("a re-run was admitted while the abandoned apply's pipe is still held: %v", err)
		}
		select {
		case <-adm.Settled():
		case <-time.After(15 * time.Second):
			t.Fatal("the run never settled after the abandoned apply exited")
		}
		if _, err := adm.Admit(); err != nil {
			t.Errorf("not admitted once settled: %v", err)
		}
	})
}
