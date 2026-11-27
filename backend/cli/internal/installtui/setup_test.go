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
		err  error
		want PhaseOutcome
	}{
		{nil, PhaseSucceeded},
		{errNotStarted, PhaseNotApproved},
		{errInstanceNotStarted, PhaseNotApproved},
		{hub.RedactError(errInstanceNotStarted), PhaseNotApproved},
		{errors.New("the instance was not started"), PhaseFailed}, // the words alone are not the verdict
		{context.Canceled, PhaseFailed},
		{hub.RedactError(errors.New("helm: timed out")), PhaseFailed},
	} {
		if got := phaseOf(c.err); got != c.want {
			t.Errorf("%v: %d, want %d", c.err, got, c.want)
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
}

func (r *stageRecorder) run(_ context.Context, p Presenter, _ installflow.Engine, req installflow.Request,
	_ installflow.PreflightRunner) error {
	i := len(r.presenters)
	r.presenters, r.requests = append(r.presenters, p), append(r.requests, req)
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
