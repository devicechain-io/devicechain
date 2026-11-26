// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"k8s.io/client-go/kubernetes/fake"
)

// lockedBuffer is a console stream: the engine writes it, the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// engineRun is one InstallEngine run, as a presenter and the process saw it.
type engineRun struct {
	sum    installflow.Summary
	err    error
	envs   []installflow.Envelope
	stdout string // the console the engine was handed
	stderr string
	leaked string // what reached the process's own stdout instead
}

// runInstallEngine runs e through a real hub with a capturing sink, the console set to
// buffers, and os.Stdout captured so a write that bypasses the console is seen.
func runInstallEngine(t *testing.T, ctx context.Context, e *InstallEngine, pf installflow.PreflightRunner,
	secrets ...string) engineRun {
	t.Helper()
	sink := &testkit.CaptureSink{}
	hub, err := installflow.NewHub(installflow.Options{Sink: sink, RunID: "engine-test",
		Clock: testkit.NewManualClock(testkit.Epoch)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if err := hub.AddSecret(s); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut lockedBuffer
	var r engineRun
	r.leaked = captureOutput(t, func() {
		r.sum, r.err = hub.Run(ctx, e, installflow.Request{Operation: installflow.OperationInstall,
			DryRun: e.Options.DryRun, AssumeYes: e.Options.AssumeYes},
			installflow.Runtime{
				Prompts:   installflow.NewBroker(hub, nil, e.Options.AssumeYes),
				Preflight: pf,
				Console:   installflow.Console{Stdout: &out, Stderr: &errOut},
			})
		<-hub.Done()
	})
	r.envs, r.stdout, r.stderr = sink.Envelopes(), out.String(), errOut.String()
	if err := testkit.VerifyStream(r.envs); err != nil {
		t.Errorf("the stream breaks the protocol: %v", err)
	}
	return r
}

// stageOutcomes is each stage attempt's ending, in stream order: "step=outcome", or
// "step=failed:code" for a failure.
func stageOutcomes(envs []installflow.Envelope) []string {
	var out []string
	for _, e := range envs {
		switch p := e.Payload.(type) {
		case installflow.StepFinished:
			out = append(out, fmt.Sprintf("%s=%s", e.StepID, p.Outcome))
		case installflow.StepFailed:
			out = append(out, fmt.Sprintf("%s=failed:%s", e.StepID, p.Code))
		}
	}
	return out
}

func sameStrings(a, b []string) bool {
	return strings.Join(a, "\n") == strings.Join(b, "\n")
}

// noCluster is a Cluster stand-in that must not be reached.
func noCluster(t *testing.T) func(context.Context, Provider, InstallOptions) error {
	return func(context.Context, Provider, InstallOptions) error {
		t.Error("the cluster stages ran")
		return nil
	}
}

// 🔴 THE REAL Install, AS A DRY RUN, THROUGH THE ENGINE: every stage in order, each
// planned or read-only, and the plan printed to the console it was HANDED — byte for
// byte what Install prints straight to stdout, with nothing reaching stdout itself.
func TestInstallEngineDryRunReportsEveryStageAndPrintsOnlyToItsConsole(t *testing.T) {
	stubIdentifyCluster(t, "", errors.New("no cluster here"))
	opts := InstallOptions{}
	direct, derr := dryRunInstall(t, opts)
	if derr != nil {
		t.Fatalf("the direct dry run failed: %v", derr)
	}

	deadKubeconfig(t)
	opts.Options = Options{DryRun: true, KubeContext: "dead", ImageRegistry: "example.invalid/dc",
		ImageVersion: "v0.0.0-test"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	r := runInstallEngine(t, ctx, &InstallEngine{
		Provider: &fakeProvider{name: "local", ensureBind: ClusterBinding{KubeContext: "dead"}},
		Options:  opts, SkipPreflight: true,
	}, nil)
	if r.err != nil {
		t.Fatalf("engine: %v", r.err)
	}
	if r.stdout != direct {
		t.Errorf("the engine's console differs from Install's own output.\nengine:\n%s\ndirect:\n%s", r.stdout, direct)
	}
	if !strings.Contains(r.stdout, "[dry-run] would install the operator") {
		t.Errorf("the dry run printed no plan:\n%s", r.stdout)
	}
	if r.leaked != "" || r.stderr != "" {
		t.Errorf("output bypassed the console: stdout %q, stderr %q", r.leaked, r.stderr)
	}
	want := []string{
		"install.inputs=unchanged", "install.preflight=not_applicable", "install.cluster=planned",
		"install.claim=planned", "install.inspect=unchanged", "install.credentials=planned",
		"install.prepare=planned", "install.write-intent=planned", "install.operator=planned",
		"install.prerequisites=planned", "install.database-login=planned", "install.record=planned",
		"install.rdb-ready=planned", "install.summary=not_applicable",
	}
	if got := stageOutcomes(r.envs); !sameStrings(got, want) {
		t.Errorf("stages:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.sum.Outcome != installflow.RunPlanned || r.sum.PartialState != installflow.EffectNoneObserved {
		t.Errorf("summary = %+v", r.sum)
	}
}

// The preflight runs as its own stage, its observations travel inside it, and a
// failure stops the run there — before anything touches a cluster — with the refusal
// the command has always printed.
func TestInstallEnginePreflightFailureStopsBeforeTheCluster(t *testing.T) {
	pf := installflow.PreflightFunc(func(_ context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		observe(installflow.PreflightObservation{Observation: installflow.ObserveSection, Section: "preflight.tooling"})
		observe(installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: installflow.CheckFail,
			Text: installflow.Text("engine memory: 4.0Gi"), Fix: installflow.Text("give Docker more")})
		observe(installflow.PreflightObservation{Observation: installflow.ObserveSummary, Fails: 1})
		return installflow.PreflightResult{Fails: 1}, nil
	})
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"},
		Cluster: noCluster(t)}, pf)
	if r.err == nil || r.err.Error() != "1 preflight check(s) failed — fix the items above, or re-run with --skip-preflight" {
		t.Fatalf("err = %v", r.err)
	}
	observations := 0
	for _, e := range r.envs {
		if _, ok := e.Payload.(installflow.PreflightObservation); ok {
			observations++
			if e.StepID != installflow.StepInstallPreflight || e.Attempt != 1 {
				t.Errorf("an observation travelled outside the preflight stage: %s/%d", e.StepID, e.Attempt)
			}
		}
	}
	if observations != 3 {
		t.Errorf("%d observations reached the stream, want 3", observations)
	}
	want := []string{"install.inputs=unchanged", "install.preflight=failed:install.preflight.failed"}
	if got := stageOutcomes(r.envs); !sameStrings(got, want) {
		t.Errorf("stages = %v, want %v", got, want)
	}
	if r.sum.Outcome != installflow.RunFailed || r.sum.FailedStep != installflow.StepInstallPreflight ||
		r.sum.PartialState != installflow.EffectNoneObserved || r.sum.Claim != installflow.ClaimNotClaimed {
		t.Errorf("summary = %+v", r.sum)
	}
}

// Warnings alone do not stop it, and the cluster stages then run.
func TestInstallEnginePreflightWarningsProceed(t *testing.T) {
	ran := false
	pf := installflow.PreflightFunc(func(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		return installflow.PreflightResult{Warns: 2}, nil
	})
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"},
		Cluster: func(context.Context, Provider, InstallOptions) error { ran = true; return nil }}, pf)
	if r.err != nil || !ran || r.sum.Outcome != installflow.RunSucceeded {
		t.Errorf("err %v, cluster ran %v, summary %+v", r.err, ran, r.sum)
	}
}

// 🔴 FAIL CLOSED: an engine asked for a preflight and handed no runner refuses, rather
// than running the install unchecked.
func TestInstallEngineWithoutAPreflightRunnerRefuses(t *testing.T) {
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"},
		Cluster: noCluster(t)}, nil)
	if r.err == nil || !strings.Contains(r.err.Error(), "no preflight runner") {
		t.Errorf("err = %v", r.err)
	}
	if r.sum.Succeeded() {
		t.Errorf("summary = %+v", r.sum)
	}
}

// The request and the options are two statements of --dry-run and --yes; a run where
// they disagree is refused before it starts.
func TestInstallEngineRefusesARequestThatContradictsItsOptions(t *testing.T) {
	sink := &testkit.CaptureSink{}
	hub, err := installflow.NewHub(installflow.Options{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	e := &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true, Cluster: noCluster(t)}
	_, err = hub.Run(t.Context(), e, installflow.Request{DryRun: true}, installflow.Runtime{})
	<-hub.Done()
	if err == nil {
		t.Fatal("a dry-run request ran a real install")
	}
	if kinds := sink.Kinds(); len(kinds) != 1 || kinds[0] != installflow.KindRunSummary {
		t.Errorf("stream = %v, want only the summary", kinds)
	}
}

// 🔴 SUCCESS ONLY AFTER THE LAST GATE. The install is recorded, the claim handed back,
// and then the relational store is waited for: a store that never joins fails the run
// at install.rdb-ready, says the record is installed and the store not ready, and never
// starts the closing report.
func TestInstallEngineAStoreThatNeverJoinsFailsAfterTheRecord(t *testing.T) {
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 1)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 1, "Creating a new replica"))
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
		Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
			return finishInstall(ctx, &State{}, "kind", typed, dyn, rec, testWaitBound, testWaitPoll)
		}}, nil)
	if r.err == nil {
		t.Fatal("the install succeeded over a store with 1 of 3 instances ready")
	}
	want := []string{"install.inputs=unchanged", "install.preflight=not_applicable",
		"install.record=succeeded", "install.rdb-ready=failed:install.rdb-ready.failed"}
	if got := stageOutcomes(r.envs); !sameStrings(got, want) {
		t.Errorf("stages:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	s := r.sum
	if s.Outcome != installflow.RunFailed || s.FailedStep != installflow.StepInstallRDBReady ||
		s.InstallRecord != installflow.RecordInstalled || s.Readiness != installflow.ReadinessNotReady ||
		s.PartialState != installflow.EffectApplied {
		t.Errorf("summary = %+v", s)
	}
	if strings.Contains(r.stdout, "prerequisites installed") {
		t.Errorf("a success report was printed:\n%s", r.stdout)
	}
}

func TestInstallEngineAJoinedStoreSucceedsAndReports(t *testing.T) {
	fakeHome(t)
	rec := aCompleteInstall()
	typed := fake.NewSimpleClientset(cnpgPods("dc-system", "dc-rdb", 3)...)
	dyn := fakeDyn(aCluster("dc-system", "dc-rdb", 3, 3, healthyClusterPhase))
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"}, SkipPreflight: true,
		Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
			return finishInstall(ctx, &State{}, "kind", typed, dyn, rec, time.Second, testWaitPoll)
		}}, nil)
	if r.err != nil {
		t.Fatal(r.err)
	}
	want := []string{"install.inputs=unchanged", "install.preflight=not_applicable",
		"install.record=succeeded", "install.rdb-ready=succeeded", "install.summary=succeeded"}
	if got := stageOutcomes(r.envs); !sameStrings(got, want) {
		t.Errorf("stages = %v, want %v", got, want)
	}
	if r.sum.Outcome != installflow.RunSucceeded || r.sum.Readiness != installflow.ReadinessReady ||
		r.sum.InstallRecord != installflow.RecordInstalled {
		t.Errorf("summary = %+v", r.sum)
	}
	if !strings.Contains(r.stdout, "Next: build an instance") || r.leaked != "" {
		t.Errorf("report went to console %q, stdout %q", r.stdout, r.leaked)
	}
}

// 🔴 AN ERROR'S TEXT NEVER ENTERS THE STREAM. The install's error is returned to the
// command, which prints it; the events say which stage failed and nothing of why, so a
// secret an error carries — a webhook URL, an SMTP password — cannot ride along.
func TestInstallEngineKeepsAFailuresTextOutOfTheStream(t *testing.T) {
	var all []string
	for _, c := range testkit.Canaries() {
		all = append(all, c.Value)
	}
	cause := errors.New("the alert route was refused: " + strings.Join(all, " | "))
	pf := installflow.PreflightFunc(func(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		return installflow.PreflightResult{}, nil
	})
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: &fakeProvider{name: "local"},
		Cluster: func(ctx context.Context, _ Provider, _ InstallOptions) error {
			runFrom(ctx).enter(installflow.StepInstallWriteIntent)
			return cause
		}}, pf)
	if !errors.Is(r.err, cause) {
		t.Fatalf("err = %v, want the install's own", r.err)
	}
	var dump []any
	for _, e := range r.envs {
		dump = append(dump, e)
	}
	testkit.AssertNoCanary(t, "the event stream", testkit.Dump(dump...))
	testkit.AssertNoCanary(t, "the summary", testkit.Dump(r.sum))
	if r.sum.FailedStep != installflow.StepInstallWriteIntent || r.sum.PartialState != installflow.EffectMayHaveChanged {
		t.Errorf("summary = %+v", r.sum)
	}
}

// The kind-cluster confirmation goes through the run's broker: a request event, the
// question by code with the cluster as its parameter, a resolution carrying only its
// disposition, and the same refusal the command has always printed when no one answers.
func TestInstallEngineAsksTheKindQuestionThroughTheBroker(t *testing.T) {
	deadKubeconfig(t)
	r := runInstallEngine(t, t.Context(), &InstallEngine{Provider: localProvider{}, SkipPreflight: true,
		Options: InstallOptions{Options: Options{Cluster: "lab", ImageRegistry: "example.invalid/dc", ImageVersion: "v1"}}}, nil)
	if r.err == nil || !strings.Contains(r.err.Error(), `no kind cluster "lab", and it was not created`) {
		t.Fatalf("err = %v", r.err)
	}
	var asked, resolved int
	for _, e := range r.envs {
		switch p := e.Payload.(type) {
		case installflow.PromptRequested:
			asked++
			if p.Question != installflow.QuestionCreateKindCluster || len(p.Params) != 1 || p.Params[0].Value.String() != "lab" {
				t.Errorf("question = %+v", p)
			}
		case installflow.PromptResolved:
			resolved++
			if p.Disposition != installflow.PromptUnavailable {
				t.Errorf("disposition = %s", p.Disposition)
			}
		}
	}
	if asked != 1 || resolved != 1 {
		t.Errorf("asked %d, resolved %d", asked, resolved)
	}
	if r.sum.FailedStep != installflow.StepInstallCluster || r.sum.PartialState != installflow.EffectNoneObserved {
		t.Errorf("summary = %+v", r.sum)
	}
}
