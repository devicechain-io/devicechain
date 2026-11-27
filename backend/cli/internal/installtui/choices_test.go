// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// replanner is a command's replan: it records each set of edits it is asked to resolve,
// and returns the run they become, whose preflight is runner(edits).
type replanner struct {
	// base is the facts the edits apply to; nil is preFacts.
	base   *Facts
	asked  []Edits
	runner func(Edits) installflow.PreflightRunner
	refuse func(Edits) error
}

func (r *replanner) replan(e Edits) (Replanned, error) {
	r.asked = append(r.asked, e.clone())
	if r.refuse != nil {
		if err := r.refuse(e); err != nil {
			return Replanned{}, err
		}
	}
	return Replanned{Engine: engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
		return succeeded, nil
	}), Preflight: r.runner(e), Facts: r.facts(e), Settings: testSettings(e)}, nil
}

func (r *replanner) facts(e Edits) Facts {
	if r.base != nil {
		return planFactsFrom(*r.base, e)
	}
	return planFacts(e)
}

// testSettings is a drawer as a command would give it: two editors (one unavailable on
// Standard), and carried flags, one of them a credential.
func testSettings(e Edits) []Setting {
	monitoringOff := e.Toggles["no-monitoring"]
	source := "default"
	if _, ok := e.Toggles["no-monitoring"]; ok {
		source = changedHere
	}
	tls := Setting{Group: "Backups and snapshots", Flag: "--no-tls", Label: "TLS and database backups", Value: "on",
		Source: "default", Toggle: true, Name: "no-tls"}
	if e.Compact == nil || !*e.Compact {
		tls.Disabled = "Standard always installs cert-manager"
	}
	value := "on"
	if monitoringOff {
		value = "off"
	}
	return []Setting{
		{Group: "Monitoring and alerts", Flag: "--no-monitoring", Label: "Monitoring stack", Value: value,
			Source: source, Toggle: true, Name: "no-monitoring", FlagValue: monitoringOff},
		tls,
		{Group: "Images and developer build", Flag: "--registry", Value: "localhost:5000", Source: "from the command line"},
		{Group: "Monitoring and alerts", Flag: "--alert-webhook", Value: "configured (hidden)", Source: "from the command line"},
		{Group: "Database placement, connection budget and WAL", Flag: "--database-toleration",
			Value: "dedicated=db:NoSchedule, spot=true:NoSchedule", Source: "from the command line"},
		{Group: "Escape hatches", Flag: "--yes", Value: "true", Source: "from the command line"},
	}
}

func passing() installflow.PreflightRunner {
	return runnerOf(preCheck(installflow.CheckPass, "kubectl v1.33.1", ""))
}

func failing() *preRunner {
	return runnerOf(preCheck(installflow.CheckFail, "port 1883 in use by mosquitto (pid 4121)", "stop what holds port 1883"))
}

// choicesWizard is a wizard whose plan passes its preflight until an edit says otherwise.
func choicesWizard(r *replanner) *Wizard {
	return &Wizard{Facts: preFacts(), Settings: testSettings(Edits{}), Replan: r.replan,
		Contexts: func() ([]string, error) { return []string{"kind-devicechain", "prod", "staging"}, nil }}
}

// toChoices goes from the welcome screen, through a passing preflight, to the cluster
// choices.
func toChoices(t *testing.T, p *prerun) {
	t.Helper()
	toPreflight(t, p)
	tabTo(t, p, actContinue)
	if out := press(p, "enter"); out != stay || p.screen != screenChoices {
		t.Fatalf("Continue on the preflight screen: %v, screen %v:\n%s", out, p.screen, screenText(p))
	}
}

// focusOn moves the choices screen's focus to it.
func focusOn(t *testing.T, p *prerun, it chItem) {
	t.Helper()
	for range len(p.items()) + 1 {
		if p.focused() == it {
			return
		}
		press(p, "tab")
	}
	t.Fatalf("Tab never reached %v", it)
}

// 🔴 THE NAMED MUTANT: a preflight that outlives the plan it was taken for. Every kind of
// change (target, name, footprint, topology, a drawer editor) must drop the results on
// screen, run the checks for the new plan with the new plan's own runner, and leave
// Continue unavailable until those have passed.
func TestAChangedChoiceInvalidatesThePreflight(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(t *testing.T, p *prerun)
		want func(Edits) bool
	}{
		{"footprint", func(t *testing.T, p *prerun) { focusOn(t, p, chFootprint); press(p, "space") },
			func(e Edits) bool { return e.Compact != nil && *e.Compact }},
		{"topology", func(t *testing.T, p *prerun) { focusOn(t, p, chTopology); press(p, "down") },
			func(e Edits) bool { return e.HA != nil && *e.HA }},
		{"existing context", func(t *testing.T, p *prerun) { focusOn(t, p, chTarget); press(p, "space", "down", "enter") },
			func(e Edits) bool { return e.Target != nil && e.Target.KubeContext == "prod" }},
		{"cluster name", func(t *testing.T, p *prerun) {
			focusOn(t, p, chName)
			press(p, "enter", "-", "2", "enter")
		}, func(e Edits) bool { return e.Target != nil && e.Target.Cluster == "devicechain-2" }},
		{"drawer editor", func(t *testing.T, p *prerun) {
			focusOn(t, p, chAdvanced)
			press(p, "enter")
			drawerTo(t, p, "--no-monitoring")
			press(p, "space", "esc")
		}, func(e Edits) bool { return e.Toggles["no-monitoring"] }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var replanned []*preRunner
			r := &replanner{runner: func(Edits) installflow.PreflightRunner {
				f := failing()
				replanned = append(replanned, f)
				return f
			}}
			first := runnerOf(preCheck(installflow.CheckPass, "kubectl v1.33.1", ""))
			p := newTestPrerun(choicesWizard(r), first)
			toChoices(t, p)
			if !p.canContinue() {
				t.Fatal("the first plan's passing preflight left Continue unavailable")
			}
			c.edit(t, p)
			if len(r.asked) != 1 || !c.want(r.asked[0]) {
				t.Fatalf("replan asked %+v", r.asked)
			}
			if len(replanned) != 1 || replanned[0].calls.Load() != 1 || first.calls.Load() != 1 {
				t.Fatalf("the changed plan's checks did not run with its own runner: first %d, replanned %v",
					first.calls.Load(), replanned)
			}
			if p.runner != replanned[0] || p.engine == nil {
				t.Fatal("the screen kept the old plan's run")
			}
			if p.canContinue() {
				t.Fatalf("Continue is available on the first plan's preflight after a change:\n%s", screenText(p))
			}
			view := screenText(p)
			if p.screen == screenChoices && !strings.Contains(view, "Continue (unavailable)") {
				t.Errorf("Continue is not shown unavailable:\n%s", view)
			}
			p.screen, p.ch.drawer = screenChoices, false
			focusOn(t, p, chContinue)
			if out := press(p, "enter"); out == proceed {
				t.Fatal("Continue started the run on a plan whose preflight failed")
			}
		})
	}
}

// While the changed plan's checks run, Continue waits for them, and says so.
func TestContinueWaitsForTheChangedPlansChecks(t *testing.T) {
	block := make(chan struct{})
	slow := installflow.PreflightFunc(func(ctx context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		observe(preCheck(installflow.CheckPass, "kubectl v1.33.1", ""))
		return installflow.PreflightResult{}, nil
	})
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return slow }}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	focusOn(t, p, chFootprint)
	cmd, _ := p.update(keyOf("space"))
	if !p.pf.running || p.canContinue() {
		t.Fatal("Continue is available while the changed plan's checks run")
	}
	if v := screenText(p); !strings.Contains(v, choicesRechecking) || !strings.Contains(v, "Continue (unavailable)") {
		t.Fatalf("the rerun is not said:\n%s", v)
	}
	focusOn(t, p, chContinue)
	if out := press(p, "enter"); out == proceed {
		t.Fatal("Continue started the run while the checks ran")
	}
	close(block)
	pump(p, cmd)
	if !p.canContinue() {
		t.Fatalf("the changed plan passed and Continue is unavailable:\n%s", screenText(p))
	}
	if out := press(p, "enter"); out != proceed {
		t.Fatalf("Continue gave %v", out)
	}
}

// A plan replaced while its checks run has them stopped, not left to finish unseen.
func TestAReplacedPlansChecksAreStopped(t *testing.T) {
	stopped := make(chan struct{})
	waiting := installflow.PreflightFunc(func(ctx context.Context, _ func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		<-ctx.Done()
		close(stopped)
		return installflow.PreflightResult{}, ctx.Err()
	})
	calls := 0
	r := &replanner{runner: func(Edits) installflow.PreflightRunner {
		calls++
		if calls == 1 {
			return waiting
		}
		return passing()
	}}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	focusOn(t, p, chFootprint)
	p.update(keyOf("space")) // the first change: its checks wait
	focusOn(t, p, chTopology)
	press(p, "space") // the second replaces it
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced plan's checks were left running")
	}
	if !p.canContinue() || len(r.asked) != 2 {
		t.Fatalf("after the second change: %d replans\n%s", len(r.asked), screenText(p))
	}
}

// A refused change leaves the plan, and the preflight that passed for it, as they were.
func TestARefusedChoiceKeepsThePlanAndItsPreflight(t *testing.T) {
	first := runnerOf(preCheck(installflow.CheckPass, "kubectl v1.33.1", ""))
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return failing() },
		refuse: func(Edits) error { return errors.New("--ha needs three schedulable nodes") }}
	p := newTestPrerun(choicesWizard(r), first)
	toChoices(t, p)
	focusOn(t, p, chTopology)
	press(p, "space")
	view := screenText(p)
	if !strings.Contains(view, "The plan was not changed: --ha needs three schedulable nodes") || p.facts.HA ||
		first.calls.Load() != 1 || p.engine != nil || !p.canContinue() {
		t.Fatalf("a refused change:\n%s", view)
	}
	if !strings.Contains(rowWith(t, view, "Topology"), "(*) Single") {
		t.Errorf("the topology moved: %q", rowWith(t, view, "Topology"))
	}
}

// Enter on a choice changes nothing and moves nowhere: a queued Enter can neither alter
// the plan nor walk the focus to Continue.
func TestAnEnterOnAChoiceChangesNothing(t *testing.T) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	for _, it := range []chItem{chTarget, chFootprint, chTopology} {
		focusOn(t, p, it)
		for range 6 {
			if out := press(p, "enter"); out != stay {
				t.Fatalf("Enter on %v gave %v", it, out)
			}
		}
		if p.focused() != it || len(r.asked) != 0 || p.ch.picker != nil {
			t.Fatalf("Enter on %v moved to %v or replanned %v", it, p.focused(), r.asked)
		}
	}
}

func TestChoicesSayWhereEachValueCameFrom(t *testing.T) {
	f := preFacts()
	f.KubeContext, f.Cluster, f.TargetFlag = "prod", "devicechain", "--kube-context prod"
	f.Compact, f.FootprintFlag, f.TopologyFlag = true, "--compact", "--ha=false"
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	r.base = &f
	w := choicesWizard(r)
	w.Facts = f
	p := newTestPrerun(w, passing())
	toChoices(t, p)
	view := screenText(p)
	for _, want := range []string{"(*) Existing context (from --kube-context prod)", "(*) Compact (from --compact)",
		"(*) Single ( ) HA (from --ha=false)", "Context    prod", "no monitoring stack"} {
		if !strings.Contains(view, want) {
			t.Errorf("the choices do not say %q:\n%s", want, view)
		}
	}
	focusOn(t, p, chFootprint)
	press(p, "space")
	view = screenText(p)
	if !strings.Contains(view, "(*) Standard ( ) Compact ("+changedHere+")") || strings.Contains(view, "no monitoring stack") {
		t.Errorf("after the change:\n%s", view)
	}
	press(p, "esc", "esc")
	if v := screenText(p); p.screen != screenWelcome || !strings.Contains(v, "Standard ("+changedHere+")") {
		t.Errorf("the welcome screen does not show the change:\n%s", v)
	}
}

func TestTheDrawerCarriesFlagsAndHidesCredentials(t *testing.T) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	focusOn(t, p, chAdvanced)
	press(p, "enter")
	view := screenText(p)
	for _, want := range []string{drawerTitle, "Images and developer build", "--registry localhost:5000 carried",
		"--alert-webhook configured (hidden) carried", "dedicated=db:NoSchedule, spot=true:NoSchedule",
		"Monitoring stack (--no-monitoring): on (default)", "unavailable: Standard always installs cert-manager",
		"Relational recovery", drawerEmptyGroup, "--yes true carried"} {
		if !strings.Contains(view, want) {
			t.Errorf("the drawer does not say %q:\n%s", want, view)
		}
	}
	// A carried row, and the unavailable editor, cannot be changed.
	drawerTo(t, p, "--registry")
	press(p, "space")
	drawerTo(t, p, "--no-tls")
	press(p, "space")
	if len(r.asked) != 0 {
		t.Fatalf("a row that is no editor replanned: %+v", r.asked)
	}
	drawerTo(t, p, "--no-monitoring")
	press(p, "space")
	if len(r.asked) != 1 || !r.asked[0].Toggles["no-monitoring"] {
		t.Fatalf("the toggle asked %+v", r.asked)
	}
	if v := screenText(p); !strings.Contains(v, "Monitoring stack (--no-monitoring): off ("+changedHere+")") {
		t.Errorf("after the toggle:\n%s", v)
	}
	press(p, "esc")
	if p.ch.drawer || p.screen != screenChoices {
		t.Fatal("Esc did not close the drawer")
	}
}

// The picker shows context names only, each sanitised and redacted, and Esc leaves the
// plan as it was.
func TestTheContextPickerShowsNamesOnly(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "canary-ctx-s3cr3t-4410"
	if err := hub.AddSecret(canary); err != nil {
		t.Fatal(err)
	}
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	w := choicesWizard(r)
	w.Contexts = func() ([]string, error) {
		return []string{"prod", "evil\x1b]0;title\x07name", "tok-" + canary}, nil
	}
	p := newPrerun(context.Background(), preCaps(terminal.L4, 120, 40), w, passing(), hub.Redact)
	toChoices(t, p)
	focusOn(t, p, chTarget)
	press(p, "space")
	raw := p.render()
	if p.ch.picker == nil || !strings.Contains(preStrip(raw), pickerTitle) || !strings.Contains(preStrip(raw), "prod") {
		t.Fatalf("no picker:\n%s", preStrip(raw))
	}
	if strings.Contains(raw, canary) || strings.Contains(raw, "\x1b]") {
		t.Fatalf("a context name reached the screen unsanitised: %q", raw)
	}
	press(p, "esc")
	if p.ch.picker != nil || len(r.asked) != 0 || p.facts.KubeContext != "" {
		t.Fatal("Esc on the picker changed the plan")
	}
	// The raw name is what the plan takes; the screen only draws it cleaned.
	press(p, "space", "down", "down", "enter")
	if len(r.asked) != 1 || r.asked[0].Target.KubeContext != "tok-"+canary {
		t.Fatalf("picked %+v", r.asked)
	}
	if strings.Contains(p.render(), canary) {
		t.Fatal("the picked context's secret reached the screen")
	}
}

func TestThePickerSaysWhenThereIsNothingToPick(t *testing.T) {
	for _, c := range []struct {
		contexts func() ([]string, error)
		want     string
	}{
		{func() ([]string, error) { return nil, nil }, pickerEmpty},
		{func() ([]string, error) { return nil, errors.New("yaml: line 3: bad indentation") },
			"The kubeconfig could not be read: yaml: line 3: bad indentation"},
	} {
		r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
		w := choicesWizard(r)
		w.Contexts = c.contexts
		p := newTestPrerun(w, passing())
		toChoices(t, p)
		focusOn(t, p, chTarget)
		press(p, "space", "enter")
		if v := screenText(p); !strings.Contains(v, c.want) || len(r.asked) != 0 {
			t.Errorf("picker:\n%s", v)
		}
	}
}

// S8 left Choose another context to this slice: it is on the preflight screen, and a
// context picked there re-runs the checks against it, there.
func TestChooseAnotherContextFromThePreflightScreen(t *testing.T) {
	ctxRunner := runnerOf(preCheck(installflow.CheckPass, "--kube-context staging exists", ""))
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return ctxRunner }}
	p := newTestPrerun(choicesWizard(r), laptopPreflight())
	toPreflight(t, p)
	tabTo(t, p, actContext)
	press(p, "enter")
	if p.ch.picker == nil {
		t.Fatalf("no picker:\n%s", screenText(p))
	}
	press(p, "down", "down", "enter")
	if len(r.asked) != 1 || r.asked[0].Target.KubeContext != "staging" || ctxRunner.calls.Load() != 1 {
		t.Fatalf("replan %+v, checks %d", r.asked, ctxRunner.calls.Load())
	}
	view := screenText(p)
	if p.screen != screenPreflight || !strings.Contains(view, "staging exists") || strings.Contains(view, "mosquitto") {
		t.Fatalf("after picking:\n%s", view)
	}
	if !p.canContinue() {
		t.Fatal("the new context's passing checks left Continue unavailable")
	}
}

func TestRenamingTheKindCluster(t *testing.T) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	focusOn(t, p, chName)
	press(p, "enter")
	for range len("devicechain") {
		press(p, "backspace")
	}
	press(p, "enter")
	if !strings.Contains(screenText(p), nameEmpty) || len(r.asked) != 0 || p.ch.naming == nil {
		t.Fatalf("an empty name:\n%s", screenText(p))
	}
	press(p, "l", "a", "b", " ", "\x07", "esc")
	if p.ch.naming != nil || len(r.asked) != 0 {
		t.Fatal("Esc applied the name")
	}
	press(p, "enter", "enter")
	if len(r.asked) != 0 {
		t.Fatal("an unchanged name replanned, throwing its passing preflight away")
	}
	press(p, "enter", "x", "enter")
	if len(r.asked) != 1 || r.asked[0].Target.Cluster != "devicechainx" || p.facts.Cluster != "devicechainx" {
		t.Fatalf("renamed %+v", r.asked)
	}
}

func TestTheChoicesScreenGoesBackAndExits(t *testing.T) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	p := newTestPrerun(choicesWizard(r), passing())
	toChoices(t, p)
	press(p, "esc")
	if p.screen != screenPreflight {
		t.Fatal("Esc did not go back to the preflight")
	}
	tabTo(t, p, actContinue)
	press(p, "enter")
	focusOn(t, p, chPreflight)
	press(p, "enter")
	if p.screen != screenPreflight {
		t.Fatal("Preflight did not go back")
	}
	tabTo(t, p, actContinue)
	press(p, "enter")
	if out := press(p, "q", "enter"); out != leave {
		t.Fatalf("q then Enter: %v", out)
	}
}

// Without a replan nothing can change: the choices are shown, and focus moves only
// between the actions.
func TestChoicesWithoutAReplanAreReadOnly(t *testing.T) {
	p := newTestPrerun(&Wizard{Facts: preFacts()}, passing())
	toChoices(t, p)
	if p.focused() != chContinue {
		t.Fatalf("focus starts on %v", p.focused())
	}
	press(p, "space", "down")
	if v := screenText(p); !strings.Contains(v, "(*) Standard") || strings.Contains(v, "Enter to rename") {
		t.Fatalf("read-only choices:\n%s", v)
	}
	if out := press(p, "enter"); out != proceed {
		t.Fatalf("Continue gave %v", out)
	}
}

// drawerTo moves the drawer's focus to the row for flag.
func drawerTo(t *testing.T, p *prerun, flag string) {
	t.Helper()
	o := p.ordered()
	for i, s := range o {
		if s.Flag == flag {
			for p.ch.drawerAt < i {
				press(p, "down")
			}
			for p.ch.drawerAt > i {
				press(p, "up")
			}
			return
		}
	}
	t.Fatalf("no drawer row for %s", flag)
}

// Every row of the drawer can be scrolled to, on the smallest screen: a carried flag is
// never out of reach.
func TestEveryDrawerRowCanBeReached(t *testing.T) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	p := newPrerun(context.Background(), preCaps(terminal.L1, 60, 16), choicesWizard(r), passing(), nil)
	p.resize(200, 40)
	toChoices(t, p)
	focusOn(t, p, chAdvanced)
	press(p, "enter")
	p.resize(60, 16)
	for _, s := range p.ordered() {
		drawerTo(t, p, s.Flag)
		if v := screenText(p); !strings.Contains(v, s.Flag) {
			t.Errorf("%s is out of reach:\n%s", s.Flag, v)
		}
	}
}
