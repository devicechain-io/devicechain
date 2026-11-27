// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
)

// cloudAdvisory is the advisory diagnostics of an existing cluster as the command reports
// them: one problem, one undecided permission, and passes.
func cloudAdvisory() *preRunner {
	return &preRunner{obs: []installflow.PreflightObservation{
		preSection(installplain.SectionCloud),
		preCheck(installflow.CheckPass, "API server name api.prod.example resolves", ""),
		preCheck(installflow.CheckPass, "API server api.prod.example answered over verified TLS", ""),
		preCheck(installflow.CheckPass, "authentication: the API server accepts this context's credentials", ""),
		preCheck(installflow.CheckWarn, "may create secrets: not permitted",
			"installing the shared prerequisites needs cluster-admin on this cluster; use a context that has it, or ask the cluster's administrator"),
		preCheck(installflow.CheckUnknown, "may create clusterroles.rbac.authorization.k8s.io: the API server could not decide", "Recheck"),
		preCheck(installflow.CheckInfo, "image registry ghcr.io answers from this machine (the cluster's nodes pull from it themselves)", ""),
	}, res: installflow.PreflightResult{Warns: 1}}
}

func prodFacts() Facts {
	f := preFacts()
	f.KubeContext, f.Cluster = "prod", ""
	return f
}

// The advisory rows follow the checks under their own heading, as words.
func TestAdvisoryRowsFollowTheChecks(t *testing.T) {
	adv := cloudAdvisory()
	p := newTestPrerun(&Wizard{Facts: prodFacts(), Advisory: adv}, healthyPreflight())
	toPreflight(t, p)
	view := screenText(p)
	if adv.calls.Load() != 1 {
		t.Fatalf("the advisory checks ran %d times", adv.calls.Load())
	}
	if strings.Index(view, "Existing cluster (advisory)") < strings.Index(view, "server v1.33.1") {
		t.Errorf("the advisory section is not after the checks:\n%s", view)
	}
	if r := rowWith(t, view, "may create secrets"); !strings.Contains(r, "WARN") {
		t.Errorf("the permission reads %q", r)
	}
	if r := rowWith(t, view, "could not decide"); !strings.Contains(r, "UNKNOWN") || strings.Contains(r, "PASS") {
		t.Errorf("the undecided permission reads %q", r)
	}
	if !p.canContinue() {
		t.Fatal("advisory warnings blocked Continue")
	}
	if !strings.Contains(view, "ready, with 1 warning and 1 check not determined") {
		t.Errorf("the tally does not count the advisory rows:\n%s", view)
	}
}

// 🔴 An advisory check can never block Continue on its own: a FAIL it reports reads WARN,
// a failure count it returns is not the run's, and its error or panic is one UNKNOWN row
// rather than the screen's error.
func TestAdvisoryNeverBlocksContinue(t *testing.T) {
	for name, adv := range map[string]installflow.PreflightRunner{
		"a fail row": runnerOf(preCheck(installflow.CheckFail, "may create namespaces: not permitted", "ask")),
		"a fail count": &preRunner{obs: []installflow.PreflightObservation{preTally(3, 0)},
			res: installflow.PreflightResult{Fails: 3}},
		"an error": &preRunner{err: errors.New("boom")},
		"a panic": installflow.PreflightFunc(func(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
			panic("boom")
		}),
	} {
		t.Run(name, func(t *testing.T) {
			p := newTestPrerun(&Wizard{Facts: prodFacts(), Advisory: adv}, healthyPreflight())
			toPreflight(t, p)
			view := screenText(p)
			if !p.canContinue() || strings.Contains(view, "FAIL") || strings.Contains(view, "could not finish: ") {
				t.Fatalf("advisory %s blocked Continue:\n%s", name, view)
			}
			if name == "an error" || name == "a panic" {
				if r := rowWith(t, view, advisoryStopped); !strings.Contains(r, "UNKNOWN") {
					t.Errorf("the stopped advisory reads %q", r)
				}
			}
		})
	}
}

// The advisory checks run only when the checks ran and finished: never under
// --skip-preflight, and never after the checks themselves errored.
func TestAdvisoryRunsOnlyAfterTheChecks(t *testing.T) {
	f := prodFacts()
	f.SkipPreflight = true
	adv := cloudAdvisory()
	p := newTestPrerun(&Wizard{Facts: f, Advisory: adv}, healthyPreflight())
	toPreflight(t, p)
	broken := &preRunner{err: errors.New("doctor broke")}
	q := newTestPrerun(&Wizard{Facts: prodFacts(), Advisory: adv}, broken)
	toPreflight(t, q)
	if adv.calls.Load() != 0 {
		t.Fatalf("the advisory checks ran %d times", adv.calls.Load())
	}
	if q.canContinue() {
		t.Fatal("the checks' own error stopped blocking Continue")
	}
}

// The run's own preflight is the doctor's alone: the advisory checks run on the screen
// and nowhere else.
func TestTheRunsPreflightIsNotTheAdvisory(t *testing.T) {
	var advisory atomic.Int32
	adv := installflow.PreflightFunc(func(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		advisory.Add(1)
		return installflow.PreflightResult{}, nil
	})
	doctor := healthyPreflight()
	h := wizardHarness(t, &Wizard{Facts: prodFacts(), Advisory: adv})
	done := h.runWith(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		if _, err := rt.Preflight.RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), doctor)
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("\r")
	waitFor(t, "the tally", h.out, screenShows(h.out, preflightAllPass))
	h.press("\x1b[Z\r") // Shift+Tab from Recheck back to Continue
	waitFor(t, "the cluster choices", h.out, screenShows(h.out, choicesTitle))
	h.press("\r") // no Replan: the choices screen offers its actions only, Continue first
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	h.press("\r")
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if advisory.Load() != 1 || doctor.calls.Load() != 2 {
		t.Fatalf("advisory ran %d times (want the screen's 1), the doctor %d (want the screen's and the run's)",
			advisory.Load(), doctor.calls.Load())
	}
}

// A replan cancels the replaced plan's advisory probes and the screen's join waits for
// them: a change of target on screen never leaves the old cluster's checks running, and
// the new plan's checks are its own.
func TestAReplanCancelsTheAdvisoryProbes(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	old := installflow.PreflightFunc(func(ctx context.Context, _ func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(10 * time.Second):
		}
		return installflow.PreflightResult{}, nil
	})
	var replanned atomic.Int32
	next := installflow.PreflightFunc(func(context.Context, func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		replanned.Add(1)
		return installflow.PreflightResult{}, nil
	})
	w := &Wizard{Facts: prodFacts(), Advisory: old, Replan: func(Edits) (Replanned, error) {
		return Replanned{Preflight: healthyPreflight(), Advisory: next, Facts: prodFacts()}, nil
	}}
	p := newTestPrerun(w, healthyPreflight())
	first := p.startChecks()
	go func() { // drain the first run, as the program would
		for cmd := first; cmd != nil; {
			msg := cmd()
			if msg == nil {
				return
			}
			m, ok := msg.(checkMsg)
			if !ok || m.next == nil {
				return
			}
			cmd = waitCheck(m.next)
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first plan's advisory checks never started")
	}
	second := p.applyEdit(func(e *Edits) { v := true; e.Compact = &v })
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("a replan left the replaced plan's advisory checks running")
	}
	pump(p, second)
	joined := make(chan struct{})
	go func() { p.wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("the screen's join did not see the replaced advisory checks end")
	}
	if replanned.Load() != 1 {
		t.Fatalf("the new plan's advisory checks ran %d times", replanned.Load())
	}
}
