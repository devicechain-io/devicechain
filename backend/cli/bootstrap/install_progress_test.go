// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installflow/testkit"
	"github.com/devicechain-io/dcctl/internal/tofujson"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// progressRun is an engine run held open at one stage, through a real hub on a manual
// clock, so the progress the code below the engine reports can be read back exactly as a
// presenter would receive it.
type progressRun struct {
	ctx   context.Context
	run   *installRun
	hub   *installflow.Hub
	sink  *testkit.CaptureSink
	clock *testkit.ManualClock
	out   *lockedBuffer
	err   *lockedBuffer
}

func newProgressRun(t *testing.T, observe bool, stage installflow.StepID) *progressRun {
	t.Helper()
	p := &progressRun{sink: &testkit.CaptureSink{}, clock: testkit.NewManualClock(testkit.Epoch),
		out: &lockedBuffer{}, err: &lockedBuffer{}}
	hub, err := installflow.NewHub(installflow.Options{Sink: p.sink, RunID: "progress-test", Clock: p.clock})
	if err != nil {
		t.Fatal(err)
	}
	p.hub = hub
	p.run = newInstallRun(installflow.Runtime{Events: hub, Clock: p.clock, ObserveTools: observe,
		Console: installflow.Console{Stdout: p.out, Stderr: p.err}}, false)
	if err := p.run.start(installflow.Target{Mode: "managed-kind"}); err != nil {
		t.Fatal(err)
	}
	p.run.enter(stage)
	p.ctx = withRun(context.Background(), p.run)
	return p
}

// finish closes the run as a success and returns the whole stream, checked against the
// protocol (which includes: a count of actions never goes back within its attempt and
// never changes its total).
func (p *progressRun) finish(t *testing.T) []installflow.Envelope {
	t.Helper()
	if _, err := p.run.close(p.ctx, nil); err != nil {
		t.Fatalf("closing the run: %v", err)
	}
	<-p.hub.Done()
	envs := p.sink.Envelopes()
	if err := testkit.VerifyStream(envs); err != nil {
		t.Errorf("the stream breaks the protocol: %v", err)
	}
	return envs
}

type seenProgress struct {
	attempt int
	installflow.StepProgress
}

func (s seenProgress) String() string {
	if s.Total == nil {
		return fmt.Sprintf("a%d %s %d/?", s.attempt, s.MetricID, s.Current)
	}
	return fmt.Sprintf("a%d %s %d/%d", s.attempt, s.MetricID, s.Current, *s.Total)
}

func progressOf(envs []installflow.Envelope, metric string) []seenProgress {
	var out []seenProgress
	for _, e := range envs {
		if sp, ok := e.Payload.(installflow.StepProgress); ok && sp.MetricID == metric {
			out = append(out, seenProgress{e.Attempt, sp})
		}
	}
	return out
}

func lastOfAttempt(ps []seenProgress, attempt int) (seenProgress, bool) {
	var last seenProgress
	found := false
	for _, p := range ps {
		if p.attempt == attempt {
			last, found = p, true
		}
	}
	return last, found
}

func readTofuFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "internal", "tofujson", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestProgressIsCoalescedAndItsLastValueArrivesBeforeTheStageEnds drives a recorded
// apply through the observer into the hub on a manual clock. Every completed action is
// reported, the hub flushes at most one value per interval, and the value a presenter
// is left holding when the stage ends is the last one OpenTofu reported — five of five
// here, because OpenTofu reported five completions, not because the stage ended.
func TestProgressIsCoalescedAndItsLastValueArrivesBeforeTheStageEnds(t *testing.T) {
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	sp := p.run.tofuProgress()
	if sp == nil {
		t.Fatal("no tofu progress inside an observed run's stage")
	}
	sp.tofu(tofujson.Progress{})
	o := tofujson.New(nil, nil, sp.tofu)
	for _, line := range strings.SplitAfter(string(readTofuFixture(t, "update-replace.jsonl")), "\n") {
		_, _ = o.Write([]byte(line))
		p.clock.Advance(30 * time.Millisecond)
	}
	o.Close()
	envs := p.finish(t)

	ps := progressOf(envs, metricTofuActions)
	if len(ps) == 0 {
		t.Fatal("no progress reached the presenter")
	}
	if ps[0].Mode != installflow.ProgressIndeterminate || ps[0].Total != nil {
		t.Errorf("the first value is %v; before the plan is read the total is unknown, not zero", ps[0])
	}
	last := ps[len(ps)-1]
	if last.Total == nil || last.Current != 5 || *last.Total != 5 {
		t.Errorf("the last value is %v, want 5/5", last)
	}
	// Coalescing: 26 lines over 26 ticks of 30ms is under 800ms, so at most ~9 flushes
	// at 100ms — and strictly fewer than the 7 snapshots plus the initial one would be
	// if nothing were held back, once the clock runs at that pace.
	if len(ps) > 9 {
		t.Errorf("%d values delivered in under 800ms; the hub must coalesce to 10 a second", len(ps))
	}
	end := -1
	for i, e := range envs {
		if _, ok := e.Payload.(installflow.StepFinished); ok && e.StepID == installflow.StepInstallPrerequisites {
			end = i
		}
	}
	for i, e := range envs {
		if sp, ok := e.Payload.(installflow.StepProgress); ok && sp.Current == 5 && i > end {
			t.Errorf("the final value arrived after the stage ended")
		}
	}
}

// TestARetryCountsAgainstItsOwnPlan is the CNPG admission retry as the stream shows it:
// the failed apply closes its attempt where it stopped (four of five, never five), and
// the retry is attempt 2, counted from an unknown total against its OWN plan (one).
func TestARetryCountsAgainstItsOwnPlan(t *testing.T) {
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	feed := func(name string) {
		sp := p.run.tofuProgress()
		sp.tofu(tofujson.Progress{})
		o := tofujson.New(nil, nil, sp.tofu)
		_, _ = o.Write(readTofuFixture(t, name))
		o.Close()
	}
	feed("failed.jsonl")
	p.run.retry("cnpg-admission-unreachable")
	feed("retried.jsonl")
	envs := p.finish(t)
	checkRetryStream(t, envs)
}

func checkRetryStream(t *testing.T, envs []installflow.Envelope) {
	t.Helper()
	ps := progressOf(envs, metricTofuActions)
	a1, ok1 := lastOfAttempt(ps, 1)
	a2, ok2 := lastOfAttempt(ps, 2)
	if !ok1 || !ok2 {
		t.Fatalf("progress for both attempts expected, got %v", ps)
	}
	if a1.Total == nil || a1.Current != 4 || *a1.Total != 5 {
		t.Errorf("the failed attempt ends at %v; it stopped at 4 of 5", a1)
	}
	if a2.Total == nil || a2.Current != 1 || *a2.Total != 1 {
		t.Errorf("the retry ends at %v; it planned one action and did it", a2)
	}
	first2 := true
	for _, sp := range ps {
		if sp.attempt != 2 {
			continue
		}
		if first2 && sp.Total != nil {
			t.Errorf("attempt 2 starts at %v; a retry starts with an unknown total, not attempt 1's", sp)
		}
		first2 = false
		if sp.Total != nil && *sp.Total != 1 {
			t.Errorf("attempt 2 reported %v: a denominator other than its own plan's", sp)
		}
	}
	var failed *installflow.StepFailed
	for _, e := range envs {
		if f, ok := e.Payload.(installflow.StepFailed); ok && e.StepID == installflow.StepInstallPrerequisites && e.Attempt == 1 {
			failed = &f
		}
	}
	if failed == nil || failed.Code != "install.prerequisites.cnpg-admission-unreachable" ||
		failed.Retry != installflow.RetryTransient || failed.Effect != installflow.EffectMayHaveChanged {
		t.Errorf("attempt 1 did not close as a transient, may-have-changed failure: %+v", failed)
	}
}

// TestNoToolObservationWithoutTheAsk: a run whose presenter did not ask for its tools
// to be observed — the plain presenter — runs OpenTofu as it always has.
func TestNoToolObservationWithoutTheAsk(t *testing.T) {
	p := newProgressRun(t, false, installflow.StepInstallPrerequisites)
	if p.run.tofuProgress() != nil {
		t.Fatal("an unobserved run would switch OpenTofu into -json mode")
	}
	p.finish(t)
	var nilRun *installRun
	if nilRun.tofuProgress() != nil || nilRun.progress() != nil {
		t.Fatal("outside an engine run nothing may be observed")
	}
}

// TestRolloutGateSnapshots: the rollout gate reports what each look confirmed, out of
// the Deployments it waits for, and a missing one keeps the count below the total.
func TestRolloutGateSnapshots(t *testing.T) {
	one := int32(1)
	ready := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", Generation: 1},
		Spec:   appsv1.DeploymentSpec{Replicas: &one},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 1, Replicas: 1, AvailableReplicas: 1}}
	targets := []deploymentRef{{"ns", "a"}, {"ns", "b"}}

	p := newProgressRun(t, false, installflow.StepInstallOperator)
	if err := waitForRollout(p.ctx, fake.NewSimpleClientset(ready.DeepCopy()), targets, 5*time.Millisecond); err == nil {
		t.Fatal("a missing Deployment rolled out")
	}
	ps := progressOf(p.finish(t), metricDeployments)
	if len(ps) == 0 {
		t.Fatal("the gate reported nothing")
	}
	for _, sp := range ps {
		if sp.Mode != installflow.ProgressMeasured || sp.Total == nil || *sp.Total != 2 || sp.Current != 1 ||
			sp.Unit != installflow.UnitDeployments {
			t.Errorf("got %v, want 1 of 2 deployments", sp)
		}
	}

	b := ready.DeepCopy()
	b.Name = "b"
	p = newProgressRun(t, false, installflow.StepInstallOperator)
	if err := waitForRollout(p.ctx, fake.NewSimpleClientset(ready.DeepCopy(), b), targets, time.Second); err != nil {
		t.Fatal(err)
	}
	ps = progressOf(p.finish(t), metricDeployments)
	if len(ps) != 1 || ps[0].Current != 2 || *ps[0].Total != 2 {
		t.Errorf("got %v, want one look at 2 of 2", ps)
	}
}

// TestStoreGateSnapshotsAreNotTheGate: the store's ready instances are reported as
// they are read — 3 of 3 included — while the wait itself still refuses a Cluster that
// is not healthy. A full count is not success.
func TestStoreGateSnapshotsAreNotTheGate(t *testing.T) {
	dyn := fakeDyn(aCluster("dc-x", "dc-rdb", 3, 3, "Creating a new replica"))
	typed := fake.NewSimpleClientset(cnpgPods("dc-x", "dc-rdb", 3)...)
	p := newProgressRun(t, false, installflow.StepInstallRDBReady)
	if err := waitForCNPGClusterReady(p.ctx, dyn, typed, "dc-x", "dc-rdb", testWaitBound, testWaitPoll); err == nil {
		t.Fatal("an unhealthy Cluster passed the gate")
	}
	ps := progressOf(p.finish(t), metricStoreInstances)
	if len(ps) == 0 {
		t.Fatal("the gate reported nothing")
	}
	last := ps[len(ps)-1]
	if last.Unit != installflow.UnitReplicas || last.Current != 3 || last.Total == nil || *last.Total != 3 {
		t.Errorf("got %v, want 3 of 3 replicas", last)
	}
}

// TestProgressFromAnotherGoroutine is the shape of the real thing: OpenTofu's stdout is
// drained on terraform-exec's goroutine while the run's own goroutine reports a gate.
// Run under -race.
func TestProgressFromAnotherGoroutine(t *testing.T) {
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	sp := p.run.tofuProgress()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		o := tofujson.New(p.out, p.err, sp.tofu)
		_, _ = o.Write(readTofuFixture(t, "create.jsonl"))
		o.Close()
	}()
	for i := int64(0); i <= 3; i++ {
		p.run.gate(metricDeployments, installflow.UnitDeployments, sourceKubernetes, i, 3)
	}
	wg.Wait()
	envs := p.finish(t)
	if last, ok := lastOfAttempt(progressOf(envs, metricTofuActions), 1); !ok || last.Current != 4 {
		t.Errorf("got %v", last)
	}
}

// fakeTofuJSON is a fake tofu whose n-th apply prints apply-<n>.jsonl and exits with
// apply-<n>.rc, and which records each apply's argv. Applies past the last fixture fail.
func fakeTofuJSON(t *testing.T, applies ...struct {
	stream []byte
	rc     int
}) (bin, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX fake tofu")
	}
	dir = t.TempDir()
	for i, a := range applies {
		n := i + 1
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("apply-%d.jsonl", n)), a.stream, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("apply-%d.rc", n)), []byte(fmt.Sprint(a.rc)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
d="$(dirname "$0")"
case "$1" in
  version) echo '{"terraform_version":"1.10.6","platform":"linux_amd64","provider_selections":{}}' ;;
  apply)
    n=$(( $(cat "$d/count" 2>/dev/null || echo 0) + 1 ))
    echo "$n" > "$d/count"
    printf '%s\n' "$@" > "$d/apply-$n.argv"
    [ -f "$d/apply-$n.jsonl" ] || { echo "no apply $n" >&2; exit 1; }
    cat "$d/apply-$n.jsonl"
    exit "$(cat "$d/apply-$n.rc")" ;;
  *) echo "unexpected subcommand $1" >&2; exit 1 ;;
esac
`
	bin = filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// cnpgRefusal is a failed apply's stream whose error diagnostic is the API server
// failing to call the CNPG webhook: in -json mode the ONLY place those words are.
func cnpgRefusal(t *testing.T) []byte {
	b := readTofuFixture(t, "failed.jsonl")
	diag := `{"@level":"error","@message":"Error: cannot create Cluster","type":"diagnostic",` +
		`"diagnostic":{"severity":"error","summary":"cannot create Cluster","detail":` +
		`"Internal error occurred: failed calling webhook \"mcluster.cnpg.io\": failed to call webhook: ` +
		`Post \"https://cnpg-webhook-service.cnpg-system.svc:443/mutate\": context deadline exceeded"}}` + "\n"
	return append(b, diag...)
}

// TestTheObservedApplyThroughTheRetry runs the real runner against a fake tofu: the
// first apply is refused at the CNPG webhook after four of five actions, the retry
// plans one and does it. It pins, end to end through applyWithCNPGAdmissionRetry:
//   - the apply ran with -json only because the run asked for it;
//   - the webhook's words still reached the classifier (the retry happened);
//   - each apply was counted against its own plan, in its own attempt;
//   - the messages' lines reached the console, and the diagnostic its stderr.
func TestTheObservedApplyThroughTheRetry(t *testing.T) {
	t.Run("ordinary", func(t *testing.T) { checkObservedApplyRetry(t, cnpgRefusal(t)) })
	t.Run("oversized", func(t *testing.T) {
		stream := strings.Replace(string(cnpgRefusal(t)), "Internal error occurred:",
			strings.Repeat("z", tofujson.MaxLine+100)+"Internal error occurred:", 1)
		checkObservedApplyRetry(t, []byte(stream))
	})
}

func checkObservedApplyRetry(t *testing.T, refusal []byte) {
	t.Helper()
	testKube(t)
	bin, dir := fakeTofuJSON(t,
		struct {
			stream []byte
			rc     int
		}{refusal, 1},
		struct {
			stream []byte
			rc     int
		}{readTofuFixture(t, "retried.jsonl"), 0})
	p := newProgressRun(t, true, installflow.StepInstallPrerequisites)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	tf.useConsole(out(p.ctx))
	waited := false
	err = applyWithCNPGAdmissionRetry(p.ctx, tf, nil, "tofu apply", func(context.Context) error {
		waited = true
		return nil
	})
	if err != nil {
		t.Fatalf("the retried apply failed: %v", err)
	}
	if !waited {
		t.Fatal("the webhook refusal was not recognised in -json mode, so nothing was retried")
	}
	for n := 1; n <= 2; n++ {
		argv, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("apply-%d.argv", n)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(argv), "-json\n") {
			t.Errorf("apply %d ran without -json:\n%s", n, argv)
		}
	}
	envs := p.finish(t)
	checkRetryStream(t, envs)
	if !strings.Contains(p.out.String(), "terraform_data.e[0]: Destruction complete") {
		t.Errorf("the messages' lines did not reach the console:\n%s", p.out.String())
	}
	if !strings.Contains(p.err.String(), "failed calling webhook") {
		t.Errorf("the diagnostic did not reach the console's stderr:\n%s", p.err.String())
	}
	if strings.Contains(p.out.String(), `"@level"`) {
		t.Errorf("raw JSON reached the console:\n%s", p.out.String())
	}
}

// TestAnUnobservedApplyIsTheApplyItAlwaysWas: inside an engine run whose presenter did
// not ask (plain), the apply runs without -json, its stdout reaches the console byte
// for byte, and no OpenTofu progress is reported.
func TestAnUnobservedApplyIsTheApplyItAlwaysWas(t *testing.T) {
	testKube(t)
	transcript := "Plan: 1 to add, 0 to change, 0 to destroy.\n\nApply complete! Resources: 1 added, 0 changed, 0 destroyed.\n"
	bin, dir := fakeTofuJSON(t, struct {
		stream []byte
		rc     int
	}{[]byte(transcript), 0})
	p := newProgressRun(t, false, installflow.StepInstallPrerequisites)
	tf, err := newTofuExec(t.TempDir(), bin, "")
	if err != nil {
		t.Fatal(err)
	}
	tf.useConsole(out(p.ctx))
	if err := tf.Apply(p.ctx); err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "apply-1.argv"))
	if strings.Contains(string(argv), "-json") {
		t.Errorf("an unobserved apply ran with -json:\n%s", argv)
	}
	if p.out.String() != transcript {
		t.Errorf("the console got:\n%q\nwant:\n%q", p.out.String(), transcript)
	}
	if ps := progressOf(p.finish(t), metricTofuActions); len(ps) != 0 {
		t.Errorf("an unobserved apply reported progress: %v", ps)
	}
}
