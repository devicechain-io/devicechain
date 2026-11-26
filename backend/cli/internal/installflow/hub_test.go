// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installflow_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
)

type fixture struct {
	hub   *installflow.Hub
	sink  *testkit.CaptureSink
	clock *testkit.ManualClock
}

func newFixture(t *testing.T, opts installflow.Options) *fixture {
	t.Helper()
	f := &fixture{sink: &testkit.CaptureSink{}, clock: testkit.NewManualClock(testkit.Epoch)}
	opts.Sink, opts.RunID = f.sink, "run-test"
	if opts.Clock == nil {
		opts.Clock = f.clock
	}
	h, err := installflow.NewHub(opts)
	if err != nil {
		t.Fatal(err)
	}
	f.hub = h
	t.Cleanup(f.sink.Release)
	return f
}

// wait blocks until the summary has been delivered.
func (f *fixture) wait(t *testing.T) []installflow.Envelope {
	t.Helper()
	select {
	case <-f.hub.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("summary never delivered; got %v", f.sink.Kinds())
	}
	return f.sink.Envelopes()
}

func manifest(steps ...installflow.StepID) []installflow.ManifestStep {
	out := make([]installflow.ManifestStep, len(steps))
	for i, s := range steps {
		out[i] = installflow.ManifestStep{ID: s}
	}
	return out
}

func started(dry bool, m []installflow.ManifestStep) installflow.Event {
	return installflow.Event{Payload: installflow.RunStarted{
		Target: installflow.Target{Mode: "kind-create", Context: installflow.Text("kind-devicechain")},
		DryRun: dry, Manifest: m,
	}}
}

func stepStart(id installflow.StepID, attempt int) installflow.Event {
	return installflow.Event{Step: id, Attempt: attempt, Payload: installflow.StepStarted{Message: installflow.MessageCode(id)}}
}

func stepDone(id installflow.StepID, attempt int, o installflow.StepOutcome) installflow.Event {
	return installflow.Event{Step: id, Attempt: attempt, Payload: installflow.StepFinished{Outcome: o}}
}

func stepFail(id installflow.StepID, attempt int) installflow.Event {
	return installflow.Event{Step: id, Attempt: attempt, Payload: installflow.StepFailed{
		Code: "test.failed", Retry: installflow.RetryTransient, Effect: installflow.EffectNoneObserved}}
}

func measured(id installflow.StepID, attempt int, metric string, cur, total int64) installflow.Event {
	return installflow.Event{Step: id, Attempt: attempt, Payload: installflow.StepProgress{
		MetricID: metric, Mode: installflow.ProgressMeasured, Current: cur, Total: &total,
		Unit: installflow.UnitResources, Source: "test"}}
}

func okSummary() installflow.Summary {
	return installflow.Summary{Outcome: installflow.RunSucceeded, InstallRecord: installflow.RecordInstalled,
		Readiness: installflow.ReadinessReady, Claim: installflow.ClaimReleased, PartialState: installflow.EffectApplied}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func summaries(envs []installflow.Envelope) (n int, last installflow.Summary) {
	for _, e := range envs {
		if s, ok := e.Payload.(installflow.Summary); ok {
			n, last = n+1, s
		}
	}
	return n, last
}

// TestFullInstallStreamKeepsTheContract plays every install stage, with
// progress, a prompt, and logs written concurrently from tool readers, and
// checks the delivered stream against every protocol invariant.
func TestFullInstallStreamKeepsTheContract(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	steps := installflow.InstallSteps()
	var script []testkit.Action
	script = append(script, testkit.Emit(started(false, manifest(steps...))))
	for _, s := range steps {
		script = append(script, testkit.Emit(stepStart(s, 1)))
		if s == installflow.StepInstallCluster {
			script = append(script, testkit.Ask("create", installflow.Prompt{Step: s, Kind: installflow.PromptConfirm,
				Question: "install.cluster.create", YesResolvable: true}))
		}
		if s == installflow.StepInstallPrerequisites {
			for i := int64(0); i <= 40; i++ {
				script = append(script, testkit.Emit(measured(s, 1, "tofu.actions", i, 40)), testkit.Advance(30*time.Millisecond))
			}
			script = append(script, testkit.WaitAck("logs"))
		}
		script = append(script, testkit.Emit(stepDone(s, 1, installflow.StepSucceeded)))
	}
	eng := &testkit.ScriptedEngine{Script: script, Clock: f.clock, Summary: okSummary()}

	go func() {
		<-eng.Reached("logs")
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				lw, err := f.hub.LogWriter(installflow.StepInstallPrerequisites, "tofu", installflow.LogInfo)
				if err != nil {
					t.Error(err)
					return
				}
				for i := 0; i < 200; i++ {
					fmt.Fprintf(lw, "writer %d line %d\n", w, i)
				}
				_ = lw.Close()
			}(w)
		}
		wg.Wait()
		eng.Ack("logs")
	}()

	broker := installflow.NewBroker(f.hub, nil, true)
	sum, err := f.hub.Run(context.Background(), eng, installflow.Request{}, installflow.Runtime{Prompts: broker})
	must(t, err)
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	if sum.Outcome != installflow.RunSucceeded || sum.ExitCode() != 0 {
		t.Fatalf("summary %+v", sum)
	}
	if envs[0].Kind != installflow.KindRunStarted || envs[len(envs)-1].Kind != installflow.KindRunSummary {
		t.Fatalf("stream runs %s .. %s", envs[0].Kind, envs[len(envs)-1].Kind)
	}
	logs := 0
	for _, e := range envs {
		if e.Kind == installflow.KindLogLine {
			logs++
		}
	}
	if logs != 800 {
		t.Fatalf("delivered %d log lines, want 800", logs)
	}
	// The last measured value reaches the presenter before its step ends.
	for i, e := range envs {
		if e.Kind == installflow.KindStepFinished && e.StepID == installflow.StepInstallPrerequisites {
			var last installflow.StepProgress
			for _, p := range envs[:i] {
				if pp, ok := p.Payload.(installflow.StepProgress); ok {
					last = pp
				}
			}
			if last.Current != 40 {
				t.Fatalf("last progress before the step finished is %d, want 40", last.Current)
			}
		}
	}
}

func TestFinishIsTerminalAndExactlyOnce(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	must(t, f.hub.Emit(stepDone("a", 1, installflow.StepSucceeded)))
	must(t, f.hub.Finish(okSummary()))

	if err := f.hub.Finish(okSummary()); !errors.Is(err, installflow.ErrFinished) {
		t.Fatalf("second Finish: %v, want ErrFinished", err)
	}
	if err := f.hub.Emit(installflow.Event{Payload: installflow.RunCancelling{Reason: installflow.CancelUser}}); !errors.Is(err, installflow.ErrFinished) {
		t.Fatalf("Emit after Finish: %v, want ErrFinished", err)
	}
	lw, err := f.hub.LogWriter("", "tool", installflow.LogInfo)
	must(t, err)
	if n, err := io.WriteString(lw, "late line\n"); err != nil || n != 10 {
		t.Fatalf("a late write must still be taken whole: %d, %v", n, err)
	}
	envs := f.wait(t)
	if n, _ := summaries(envs); n != 1 {
		t.Fatalf("%d summaries", n)
	}
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	if err := f.hub.Emit(installflow.Event{Payload: installflow.Summary{}}); err == nil {
		t.Fatal("a summary must not be sendable through Emit")
	}
}

type funcEngine func(context.Context, installflow.Request, installflow.Runtime) (installflow.Summary, error)

func (f funcEngine) Run(ctx context.Context, r installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
	return f(ctx, r, rt)
}

func TestRunGuaranteesOneSummary(t *testing.T) {
	t.Run("engine finished itself", func(t *testing.T) {
		f := newFixture(t, installflow.Options{})
		_, err := f.hub.Run(context.Background(), funcEngine(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
			_ = rt.Events.Emit(started(false, manifest("a")))
			return okSummary(), rt.Events.Finish(okSummary())
		}), installflow.Request{}, installflow.Runtime{})
		must(t, err)
		if n, _ := summaries(f.wait(t)); n != 1 {
			t.Fatalf("%d summaries", n)
		}
	})
	t.Run("engine returned an error claiming success", func(t *testing.T) {
		f := newFixture(t, installflow.Options{})
		sum, err := f.hub.Run(context.Background(), funcEngine(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
			_ = rt.Events.Emit(started(false, manifest("a")))
			return okSummary(), errors.New("boom")
		}), installflow.Request{}, installflow.Runtime{})
		if err == nil || sum.Outcome != installflow.RunFailed {
			t.Fatalf("outcome %s, err %v: an error never closes as success", sum.Outcome, err)
		}
		if n, last := summaries(f.wait(t)); n != 1 || last.Outcome != installflow.RunFailed {
			t.Fatalf("%d summaries, last %s", n, last.Outcome)
		}
	})
	t.Run("engine panicked", func(t *testing.T) {
		f := newFixture(t, installflow.Options{})
		func() {
			defer func() {
				if recover() == nil {
					t.Fatal("the panic must continue")
				}
			}()
			_, _ = f.hub.Run(context.Background(), funcEngine(func(_ context.Context, _ installflow.Request, rt installflow.Runtime) (installflow.Summary, error) {
				_ = rt.Events.Emit(started(false, manifest("a")))
				_ = rt.Events.Emit(stepStart("a", 1))
				panic("engine bug")
			}), installflow.Request{}, installflow.Runtime{})
		}()
		envs := f.wait(t)
		if err := testkit.VerifyStream(envs); err != nil {
			t.Fatal(err)
		}
		if _, last := summaries(envs); last.Outcome != installflow.RunFailed {
			t.Fatalf("outcome %s", last.Outcome)
		}
	})
}

func TestFinishClosesWhatTheEngineLeftOpen(t *testing.T) {
	m := []installflow.ManifestStep{{ID: "parent"}, {ID: "child", Parent: "parent"}}
	for _, tc := range []struct {
		name          string
		outcome       installflow.RunOutcome
		wantOutcome   installflow.RunOutcome
		wantCancelled bool
	}{
		{"success claimed", installflow.RunSucceeded, installflow.RunFailed, false},
		{"cancelled", installflow.RunCancelled, installflow.RunCancelled, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, installflow.Options{})
			must(t, f.hub.Emit(started(false, m)))
			must(t, f.hub.Emit(stepStart("parent", 1)))
			must(t, f.hub.Emit(installflow.Event{Step: "child", Parent: "parent", Attempt: 1,
				Payload: installflow.StepStarted{Message: "child"}}))
			s := okSummary()
			s.Outcome = tc.outcome
			if err := f.hub.Finish(s); !errors.Is(err, installflow.ErrLifecycle) {
				t.Fatalf("Finish with open steps: %v", err)
			}
			envs := f.wait(t)
			if err := testkit.VerifyStream(envs); err != nil {
				t.Fatal(err)
			}
			var closed []installflow.StepID
			for _, e := range envs {
				if p, ok := e.Payload.(installflow.StepFailed); ok {
					closed = append(closed, e.StepID)
					if p.Cancelled != tc.wantCancelled {
						t.Fatalf("cancelled=%v", p.Cancelled)
					}
				}
			}
			if fmt.Sprint(closed) != "[child parent]" {
				t.Fatalf("closed %v, want children first", closed)
			}
			if _, last := summaries(envs); last.Outcome != tc.wantOutcome {
				t.Fatalf("outcome %s, want %s", last.Outcome, tc.wantOutcome)
			}
		})
	}
}

func TestLifecycleRefusals(t *testing.T) {
	m := []installflow.ManifestStep{{ID: "parent"}, {ID: "child", Parent: "parent"}, {ID: "other"}}
	cases := []struct {
		name  string
		dry   bool
		setup []installflow.Event
		bad   installflow.Event
	}{
		{"event before run.started", false, nil, stepStart("parent", 1)},
		{"second run.started", false, nil, started(false, m)},
		{"step not in manifest", false, nil, stepStart("nope", 1)},
		{"attempt skipped", false, nil, stepStart("parent", 2)},
		{"child before parent", false, nil, installflow.Event{Step: "child", Parent: "parent", Attempt: 1, Payload: installflow.StepStarted{Message: "c"}}},
		{"parent differs from manifest", false, []installflow.Event{stepStart("other", 1)}, installflow.Event{Step: "child", Parent: "other", Attempt: 1, Payload: installflow.StepStarted{Message: "c"}}},
		{"start while active", false, []installflow.Event{stepStart("parent", 1)}, stepStart("parent", 2)},
		{"parent ends before child", false, []installflow.Event{stepStart("parent", 1), {Step: "child", Parent: "parent", Attempt: 1, Payload: installflow.StepStarted{Message: "c"}}}, stepDone("parent", 1, installflow.StepSucceeded)},
		{"finish an inactive step", false, nil, stepDone("parent", 1, installflow.StepSucceeded)},
		{"finish twice", false, []installflow.Event{stepStart("other", 1), stepDone("other", 1, installflow.StepSucceeded)}, stepDone("other", 1, installflow.StepSucceeded)},
		{"progress outside the attempt", false, nil, measured("other", 1, "m", 1, 2)},
		{"progress of an old attempt", false, []installflow.Event{stepStart("other", 1), stepFail("other", 1), stepStart("other", 2)}, measured("other", 1, "m", 1, 2)},
		{"dry run succeeds a step", true, []installflow.Event{stepStart("other", 1)}, stepDone("other", 1, installflow.StepSucceeded)},
		{"real run plans a step", false, []installflow.Event{stepStart("other", 1)}, stepDone("other", 1, installflow.StepPlanned)},
		{"prompt resolved unasked", false, nil, installflow.Event{Payload: installflow.PromptResolved{ID: "p1", Disposition: installflow.PromptConfirmed}}},
		{"free text in a code", false, []installflow.Event{stepStart("other", 1)}, installflow.Event{Step: "other", Attempt: 1, Payload: installflow.StepFailed{Code: "Error: dial tcp 10.0.0.1", Retry: installflow.RetryUnknown, Effect: installflow.EffectNoneObserved}}},
		{"unknown enum", false, []installflow.Event{stepStart("other", 1)}, stepDone("other", 1, "done")},
		{"indeterminate with total", false, []installflow.Event{stepStart("other", 1)}, installflow.Event{Step: "other", Attempt: 1, Payload: installflow.StepProgress{MetricID: "m", Mode: installflow.ProgressIndeterminate, Total: new(int64), Unit: installflow.UnitActions, Source: "s"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, installflow.Options{})
			if tc.name != "event before run.started" {
				must(t, f.hub.Emit(started(tc.dry, m)))
			}
			for _, e := range tc.setup {
				must(t, f.hub.Emit(e))
			}
			before := accepted(f.hub.Stats())
			if err := f.hub.Emit(tc.bad); err == nil {
				t.Fatal("accepted")
			}
			if after := accepted(f.hub.Stats()); after != before {
				t.Fatal("a refused event was queued")
			}
		})
	}
}

// accepted counts every event the hub has taken, delivered or not, from one
// consistent snapshot.
func accepted(s installflow.Stats) int {
	return s.ControlPending + s.LogsPending + s.ProgressPending + int(s.Delivered)
}

func TestSummaryMustMatchDryRunMode(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(true, manifest("a"))))
	if err := f.hub.Finish(okSummary()); err == nil {
		t.Fatal("a dry run finished as succeeded")
	}
	if _, last := summaries(f.wait(t)); last.Outcome != installflow.RunFailed {
		t.Fatalf("outcome %s", last.Outcome)
	}
}

func TestInvalidSummaryStillClosesTheStreamAsFailure(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	if err := f.hub.Finish(installflow.Summary{Outcome: "great"}); err == nil {
		t.Fatal("accepted")
	}
	_, last := summaries(f.wait(t))
	if last.Outcome != installflow.RunFailed || last.Readiness != installflow.ReadinessUnknown || last.Claim != installflow.ClaimUnknown {
		t.Fatalf("summary %+v", last)
	}
}

func TestProgressIsCoalescedAndFlushedBeforeTheStepEnds(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	f.sink.Hold() // nothing is delivered while we emit, so coalescing is visible
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	for i := int64(1); i <= 1000; i++ {
		must(t, f.hub.Emit(measured("a", 1, "m", i, 1000)))
	}
	if st := f.hub.Stats(); st.ProgressPending != 1 {
		t.Fatalf("%d progress values queued for one metric, want 1", st.ProgressPending)
	}
	f.clock.Advance(installflow.DefaultProgressInterval) // the held value flushes and replaces the queued one
	if st := f.hub.Stats(); st.ProgressPending != 1 {
		t.Fatalf("%d progress values queued after a flush, want 1", st.ProgressPending)
	}
	for i := int64(1); i <= 5; i++ {
		must(t, f.hub.Emit(measured("a", 1, "m", 1000-i, 1000))) // readiness regresses
	}
	must(t, f.hub.Emit(stepDone("a", 1, installflow.StepSucceeded)))
	must(t, f.hub.Finish(okSummary()))
	f.sink.Release()
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, e := range envs {
		if p, ok := e.Payload.(installflow.StepProgress); ok {
			got = append(got, p.Current)
		}
	}
	if len(got) == 0 || got[len(got)-1] != 995 {
		t.Fatalf("progress delivered %v; the final value 995 must precede the step's end", got)
	}
	if len(got) > 3 {
		t.Fatalf("progress delivered %d times for one metric in one interval: %v", len(got), got)
	}
}

func TestRetryStartsWithAFreshDenominator(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	must(t, f.hub.Emit(measured("a", 1, "m", 1, 10)))
	must(t, f.hub.Emit(measured("a", 1, "m", 9, 10))) // held
	must(t, f.hub.Emit(stepFail("a", 1)))
	must(t, f.hub.Emit(stepStart("a", 2)))
	if p := f.hub.Projection().Steps[0]; p.Attempt != 2 || len(p.Progress) != 0 {
		t.Fatalf("attempt 2 inherited attempt 1's progress: %+v", p)
	}
	must(t, f.hub.Emit(measured("a", 2, "m", 0, 4)))
	f.clock.Advance(time.Second)
	must(t, f.hub.Emit(stepDone("a", 2, installflow.StepSucceeded)))
	must(t, f.hub.Finish(okSummary()))
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	for _, e := range envs {
		if p, ok := e.Payload.(installflow.StepProgress); ok && e.Attempt == 2 && *p.Total != 4 {
			t.Fatalf("attempt 2 carried attempt 1's counts: %+v", p)
		}
	}
}

// TestSlowSinkNeverBlocksLogsOrProgress stalls the presenter completely and
// floods the hub: every call returns at once, and what waits is bounded.
func TestSlowSinkNeverBlocksLogsOrProgress(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	f.sink.Hold()
	must(t, f.hub.Emit(started(false, manifest("a"))))
	must(t, f.hub.Emit(stepStart("a", 1)))
	lw, err := f.hub.LogWriter("a", "kind", installflow.LogInfo)
	must(t, err)
	line := strings.Repeat("x", 1000) + "\n"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20000; i++ {
			_, _ = io.WriteString(lw, line)
			_ = f.hub.Emit(measured("a", 1, "m", int64(i), 20000))
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a stalled presenter blocked the engine's logs or progress")
	}
	st := f.hub.Stats()
	if st.LogsPending > installflow.DefaultLogLines {
		t.Fatalf("%d log lines wait for the presenter; the bound is %d", st.LogsPending, installflow.DefaultLogLines)
	}
	if st.LogsPending*1000 > installflow.DefaultLogBytes {
		t.Fatalf("%d bytes of logs wait for the presenter; the bound is %d", st.LogsPending*1000, installflow.DefaultLogBytes)
	}
	if st.ProgressPending > 1 {
		t.Fatalf("%d progress values wait for one metric", st.ProgressPending)
	}
	if st.LogsUndelivered == 0 {
		t.Fatal("the overflow was not counted")
	}
	snap := f.hub.Logs()
	if len(snap.Lines) > installflow.DefaultLogLines || snap.Evicted == 0 {
		t.Fatalf("ring holds %d lines, evicted %d", len(snap.Lines), snap.Evicted)
	}
	must(t, f.hub.Emit(stepDone("a", 1, installflow.StepSucceeded)))
	must(t, f.hub.Finish(okSummary()))
	f.sink.Release()
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	if _, last := summaries(envs); last.Outcome != installflow.RunSucceeded {
		t.Fatalf("dropping log lines must not change the outcome: %s", last.Outcome)
	}
}

// TestSlowSinkControlEventsHaveABoundedGrace: a control event waits for queue
// space at most the grace period, then the hub requests a cancel once and the
// run can no longer report success.
func TestSlowSinkControlEventsHaveABoundedGrace(t *testing.T) {
	var cancels int
	var mu sync.Mutex
	const queue = 8
	f := newFixture(t, installflow.Options{ControlQueue: queue, ControlGrace: 50 * time.Millisecond,
		OnDeliveryFailure: func() { mu.Lock(); cancels++; mu.Unlock() }})
	// Prompts fill the queue: unlike steps, an open prompt does not by itself
	// stop the run from succeeding, so only the stalled delivery can.
	prompt := func(i int) installflow.Event {
		return installflow.Event{Payload: installflow.PromptRequested{
			ID: installflow.PromptID(fmt.Sprintf("p%d", i)), Prompt: installflow.PromptConfirm, Question: "q"}}
	}
	f.sink.Hold()
	must(t, f.hub.Emit(started(false, manifest("a"))))
	// The pump may already hold run.started in Deliver; fill to the bound.
	deadline := time.Now().Add(5 * time.Second)
	for f.hub.Stats().Delivered == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	i := 0
	for f.hub.Stats().ControlPending < queue {
		must(t, f.hub.Emit(prompt(i)))
		i++
	}
	t0 := time.Now()
	err := f.hub.Emit(prompt(i))
	waited := time.Since(t0)
	if !errors.Is(err, installflow.ErrDeliveryStalled) {
		t.Fatalf("emit past the bound: %v, want ErrDeliveryStalled", err)
	}
	if waited > 5*time.Second {
		t.Fatalf("the engine was blocked %v", waited)
	}
	t0 = time.Now()
	if err := f.hub.Emit(prompt(i + 1)); !errors.Is(err, installflow.ErrDeliveryStalled) {
		t.Fatalf("second emit: %v", err)
	}
	if time.Since(t0) > 40*time.Millisecond {
		t.Fatal("once delivery has failed, later control events must not wait again")
	}
	if st := f.hub.Stats(); st.ControlPending > queue || !st.DeliveryFailed || st.ControlUndelivered != 2 {
		t.Fatalf("stats %+v", st)
	}
	must(t, f.hub.Emit(installflow.Event{Payload: installflow.LogLine{Level: installflow.LogInfo, Source: "x", Text: installflow.Text("still fine")}}))
	_ = f.hub.Finish(okSummary()) // the engine claims success
	f.sink.Release()
	envs := f.wait(t)
	mu.Lock()
	defer mu.Unlock()
	if cancels != 1 {
		t.Fatalf("delivery failure requested %d cancels, want exactly 1", cancels)
	}
	n, last := summaries(envs)
	if n != 1 || envs[len(envs)-1].Kind != installflow.KindRunSummary {
		t.Fatal("the summary must still be delivered, once and last")
	}
	if last.Outcome != installflow.RunFailed || !last.DeliveryIncomplete || last.ExitCode() != 1 {
		t.Fatalf("summary %+v: a run whose events were not delivered never succeeds", last)
	}
}

func TestDefaultBoundsMatchTheSpecification(t *testing.T) {
	if installflow.DefaultControlQueue != 256 || installflow.DefaultLogLines != 2000 ||
		installflow.DefaultLogBytes != 1<<20 || installflow.MaxTextBytes != 8<<10 ||
		installflow.DefaultProgressInterval != 100*time.Millisecond {
		t.Fatal("a bound moved")
	}
}

func TestCancellationClosesActiveStepsAsCancelled(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	m := []installflow.ManifestStep{{ID: "outer"}, {ID: "inner", Parent: "outer"}}
	eng := &testkit.ScriptedEngine{Script: []testkit.Action{
		testkit.Emit(started(false, m)),
		testkit.Emit(stepStart("outer", 1)),
		testkit.Emit(installflow.Event{Step: "inner", Parent: "outer", Attempt: 1, Payload: installflow.StepStarted{Message: "inner"}}),
		testkit.WaitAck("tool"),
		testkit.Emit(stepDone("inner", 1, installflow.StepSucceeded)),
	}, Summary: okSummary()}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-eng.Reached("tool"); cancel() }()
	sum, err := f.hub.Run(ctx, eng, installflow.Request{}, installflow.Runtime{})
	if !errors.Is(err, context.Canceled) || !eng.Cancelled() {
		t.Fatalf("err %v, cancelled %v", err, eng.Cancelled())
	}
	envs := f.wait(t)
	if err := testkit.VerifyStream(envs); err != nil {
		t.Fatal(err)
	}
	kinds := fmt.Sprint(f.sink.Kinds()[3:])
	if kinds != "[run.cancelling step.failed step.failed run.summary]" {
		t.Fatalf("tail %s", kinds)
	}
	for _, e := range envs {
		if p, ok := e.Payload.(installflow.StepFailed); ok && !p.Cancelled {
			t.Fatalf("%s closed without cancelled", e.StepID)
		}
	}
	if sum.Outcome != installflow.RunCancelled || sum.ExitCode() != 1 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestOneCancelPerRun(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	for i := 0; i < 3; i++ {
		must(t, f.hub.Emit(installflow.Event{Payload: installflow.RunCancelling{Reason: installflow.CancelSignal}}))
	}
	s := okSummary()
	s.Outcome = installflow.RunCancelled
	must(t, f.hub.Finish(s))
	n := 0
	for _, k := range kindsOf(f.wait(t)) {
		if k == installflow.KindRunCancelling {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d run.cancelling events", n)
	}
}

func kindsOf(envs []installflow.Envelope) []installflow.Kind {
	out := make([]installflow.Kind, len(envs))
	for i, e := range envs {
		out[i] = e.Kind
	}
	return out
}

func TestEnvelopeFields(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	f.clock.Advance(1500 * time.Millisecond)
	must(t, f.hub.Emit(stepStart("a", 1)))
	f.clock.Advance(2 * time.Second)
	must(t, f.hub.Emit(stepDone("a", 1, installflow.StepUnchanged)))
	must(t, f.hub.Finish(okSummary()))
	envs := f.wait(t)
	e := envs[2]
	if e.Schema != installflow.Schema || e.RunID != "run-test" || e.Seq != 3 || e.ElapsedMS != 3500 ||
		e.Operation != installflow.OperationInstall || e.Attempt != 1 || e.StepID != "a" {
		t.Fatalf("envelope %+v", e)
	}
	if d := e.Payload.(installflow.StepFinished).DurationMS; d != 2000 {
		t.Fatalf("duration %d: the hub measures it", d)
	}
	if e.Time.Location() != time.UTC {
		t.Fatal("time is not UTC")
	}
	j, err := e.MarshalJSON()
	must(t, err)
	want := `{"schema":"dcctl.install.events/v1","run_id":"run-test","seq":3,"time":"2026-11-25T12:00:03.5Z","elapsed_ms":3500,"operation":"install","kind":"step.finished","step_id":"a","attempt":1,"payload":{"outcome":"unchanged","duration_ms":2000}}`
	if string(j) != want {
		t.Fatalf("json\n got %s\nwant %s", j, want)
	}
}

// Flush returns only once everything emitted before it has reached the sink: a
// presenter that writes directly between events relies on it for its ordering.
func TestFlushWaitsForEarlierEvents(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	f.sink.Hold()
	must(t, f.hub.Emit(stepStart("a", 1)))
	flushed := make(chan struct{})
	go func() { f.hub.Flush(); close(flushed) }()
	select {
	case <-flushed:
		t.Fatal("Flush returned while an earlier event was undelivered")
	case <-time.After(50 * time.Millisecond):
	}
	f.sink.Release()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("Flush did not return after delivery")
	}
	if kinds := f.sink.Kinds(); len(kinds) != 2 {
		t.Errorf("delivered %v", kinds)
	}
}

// A preflight observation belongs to an active step attempt, like progress.
func TestPreflightObservationNeedsAnActiveStep(t *testing.T) {
	f := newFixture(t, installflow.Options{})
	must(t, f.hub.Emit(started(false, manifest("a"))))
	o := installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: installflow.CheckPass,
		Text: installflow.Text("docker")}
	if err := f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: o}); !errors.Is(err, installflow.ErrLifecycle) {
		t.Errorf("an observation outside its step: %v", err)
	}
	must(t, f.hub.Emit(stepStart("a", 1)))
	must(t, f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: o}))
	bad := o
	bad.Verdict = "maybe"
	if err := f.hub.Emit(installflow.Event{Step: "a", Attempt: 1, Payload: bad}); !errors.Is(err, installflow.ErrInvalidEvent) {
		t.Errorf("an unknown verdict: %v", err)
	}
}
