// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// preRunner is a preflight that reports a fixed script and counts its runs.
type preRunner struct {
	obs   []installflow.PreflightObservation
	res   installflow.PreflightResult
	err   error
	calls atomic.Int32
}

func (r *preRunner) RunPreflight(_ context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
	r.calls.Add(1)
	for _, o := range r.obs {
		observe(o)
	}
	return r.res, r.err
}

func preSection(code installflow.MessageCode) installflow.PreflightObservation {
	return installflow.PreflightObservation{Observation: installflow.ObserveSection, Section: code}
}

func preCheck(v installflow.CheckVerdict, text, fix string) installflow.PreflightObservation {
	return installflow.PreflightObservation{Observation: installflow.ObserveCheck, Verdict: v,
		Text: installflow.Text(text), Fix: installflow.Text(fix)}
}

func preTally(fails, warns int) installflow.PreflightObservation {
	return installflow.PreflightObservation{Observation: installflow.ObserveSummary, Fails: fails, Warns: warns}
}

// runnerOf scripts a run: the observations, with the counts the doctor would return.
func runnerOf(obs ...installflow.PreflightObservation) *preRunner {
	r := &preRunner{obs: obs}
	for _, o := range obs {
		if o.Observation != installflow.ObserveCheck {
			continue
		}
		switch o.Verdict {
		case installflow.CheckFail:
			r.res.Fails++
		case installflow.CheckWarn:
			r.res.Warns++
		}
	}
	r.obs = append(r.obs, preTally(r.res.Fails, r.res.Warns))
	return r
}

const capacityFix = "give Docker >= 4 CPUs and >= 8Gi, or re-run install with --compact"

func preFacts() Facts {
	return Facts{Version: "dcctl v1.12.0", Provider: "local", Providers: []string{"local"},
		Cluster: "devicechain", Images: "published images v1.12.0 from ghcr.io/devicechain-io"}
}

func preCaps(level terminal.Level, w, h int) terminal.Capabilities {
	return terminal.Capabilities{Eligible: true, TUI: true, Level: level, Unicode: level != terminal.L2,
		Width: w, Height: h, SizeKnown: true}
}

func newTestPrerun(w *Wizard, runner installflow.PreflightRunner) *prerun {
	// Wide enough that no sentence the tests look for is wrapped.
	return newPrerun(context.Background(), preCaps(terminal.L4, 200, 40), w, runner, nil)
}

// pump runs a command and feeds what it produces back in, until the screen is idle.
func pump(p *prerun, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		cmd, _ = p.update(msg)
	}
}

func keyOf(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: name}
}

// press sends keys and runs whatever they start; it returns the last key's outcome.
func press(p *prerun, keys ...string) prerunOutcome {
	out := stay
	for _, k := range keys {
		var cmd tea.Cmd
		cmd, out = p.update(keyOf(k))
		pump(p, cmd)
	}
	return out
}

// toPreflight leaves the welcome screen and lets the checks finish.
func toPreflight(t *testing.T, p *prerun) {
	t.Helper()
	if out := press(p, "enter"); out != stay || p.screen != screenPreflight {
		t.Fatalf("Enter on the welcome screen: outcome %v, screen %v", out, p.screen)
	}
}

// tabTo moves the action focus to label, failing if no Tab reaches it.
func tabTo(t *testing.T, p *prerun, a pfAction) {
	t.Helper()
	for range 8 {
		if p.pf.action == a {
			return
		}
		press(p, "tab")
	}
	t.Fatalf("Tab never reached %s; actions %v", actionLabel(a, p.pf.compact), p.pf.action)
}

func screenText(p *prerun) string { return preStrip(p.render()) }

var preSGR = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// preStrip is a screen without its colour and attribute sequences.
func preStrip(s string) string { return preSGR.ReplaceAllString(s, "") }

// rowWith is the screen line holding s.
func rowWith(t *testing.T, view, s string) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, s) {
			return l
		}
	}
	t.Fatalf("no line with %q in:\n%s", s, view)
	return ""
}

// 🔴 THE NAMED MUTANT: a FAIL rendered, counted or gated as a WARN. A failing check
// must read FAIL on its own row, and Continue must be unavailable, with no key that
// gets past it.
func TestAFailReadsFailAndBlocksContinue(t *testing.T) {
	r := runnerOf(preSection(installplain.SectionDockerEngine),
		preCheck(installflow.CheckPass, "docker daemon reachable", ""),
		preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix),
		preCheck(installflow.CheckWarn, "storage driver: vfs", "overlay2 is recommended for kind"))
	p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
	toPreflight(t, p)
	view := screenText(p)
	row := rowWith(t, view, "engine memory: 6.0Gi")
	if !regexp.MustCompile(`\bFAIL\b`).MatchString(row) || strings.Contains(row, "WARN") {
		t.Errorf("the failing check reads %q", row)
	}
	if w := rowWith(t, view, "storage driver: vfs"); !strings.Contains(w, "WARN") {
		t.Errorf("the warning reads %q", w)
	}
	// The failure is focused, so its exact fix is open.
	if !strings.Contains(view, "fix: "+capacityFix) {
		t.Errorf("the failure's fix is not shown:\n%s", view)
	}
	if !strings.Contains(view, "Continue (unavailable)") || !strings.Contains(view, preflightBlocked) {
		t.Errorf("Continue is not shown unavailable:\n%s", view)
	}
	if p.canContinue() {
		t.Fatal("Continue is available with a failing check")
	}
	// No key, in any order, starts the run past a failure.
	for _, keys := range [][]string{{"enter"}, {"tab", "enter"}, {"tab", "tab", "enter"}, {"shift+tab", "enter"},
		{"tab", "tab", "tab", "enter"}, {"down", "enter"}, {"up", "tab", "enter"}} {
		p.pf.confirm = false
		p.screen = screenPreflight
		for _, k := range keys {
			if out := press(p, k); out == proceed {
				t.Fatalf("keys %v proceeded past a failing check", keys)
			}
		}
	}
}

// Each signal of a failure blocks Continue on its own: the runner's count with no FAIL
// row, the closing tally with no FAIL row, and a FAIL row whose counts say nothing.
func TestEachFailureSignalAloneBlocksContinue(t *testing.T) {
	for _, r := range []*preRunner{
		{obs: []installflow.PreflightObservation{preCheck(installflow.CheckPass, "kubectl", "")},
			res: installflow.PreflightResult{Fails: 1}},
		{obs: []installflow.PreflightObservation{preCheck(installflow.CheckPass, "kubectl", ""), preTally(1, 0)}},
		{obs: []installflow.PreflightObservation{preCheck(installflow.CheckFail, "kubectl not found", "install kubectl")}},
	} {
		p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
		toPreflight(t, p)
		if p.canContinue() {
			t.Fatalf("Continue is available after a run that counted a failure: %+v", r.res)
		}
		if v := screenText(p); !strings.Contains(v, "1 failure") {
			t.Errorf("the tally does not say the failure:\n%s", v)
		}
	}
}

// A warning is shown, and does not block: Continue is reached with Tab and starts the run.
func TestAWarningDoesNotBlockContinue(t *testing.T) {
	r := runnerOf(preCheck(installflow.CheckWarn, "storage driver: vfs", "overlay2 is recommended for kind"))
	p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
	toPreflight(t, p)
	if !p.canContinue() {
		t.Fatal("a warning blocked Continue")
	}
	// Continue is never focused for you: an Enter typed while the checks ran must not
	// be what starts the install.
	if p.pf.action == actContinue {
		t.Fatal("the checks finished with Continue focused")
	}
	tabTo(t, p, actContinue)
	if out := press(p, "enter"); out != proceed {
		t.Fatalf("Continue gave %v", out)
	}
}

func TestEveryVerdictIsAWord(t *testing.T) {
	for v, want := range map[installflow.CheckVerdict]string{
		installflow.CheckPass: "PASS", installflow.CheckInfo: "INFO", installflow.CheckWarn: "WARN",
		installflow.CheckFail: "FAIL", installflow.CheckUnknown: "UNKNOWN", installflow.CheckNotNeeded: "NOT NEEDED",
		installflow.CheckNotChecked: "NOT CHECKED", "surprising": "SURPRISING",
	} {
		if got := verdictWord(v); got != want {
			t.Errorf("%s reads %q, want %q", v, got, want)
		}
		if cellWidth(verdictWord(v)) > verdictColumn && v != "surprising" {
			t.Errorf("%s is wider than the verdict column", v)
		}
	}
	// Unknown and not-needed are never shown as a pass, and an unknown is focused like
	// a warning so its text is in view.
	r := runnerOf(preCheck(installflow.CheckNotNeeded, "engine memory", ""),
		preCheck(installflow.CheckUnknown, "engine CPUs", "could not be read"))
	p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
	toPreflight(t, p)
	view := screenText(p)
	if row := rowWith(t, view, "engine CPUs"); !strings.Contains(row, "UNKNOWN") || strings.Contains(row, "PASS") {
		t.Errorf("unknown reads %q", row)
	}
	if row := rowWith(t, view, "engine memory"); !strings.Contains(row, "NOT NEEDED") {
		t.Errorf("not needed reads %q", row)
	}
	if !strings.Contains(view, "fix: could not be read") {
		t.Errorf("the unknown check is not focused:\n%s", view)
	}
}

// The focused row, and only it, shows its fix; moving the focus moves the fix.
func TestTheFocusedRowExpandsToItsExactFix(t *testing.T) {
	r := runnerOf(preCheck(installflow.CheckWarn, "docker context = colima", "docker context use default"),
		preCheck(installflow.CheckWarn, "storage driver: vfs", "overlay2 is recommended for kind"))
	p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
	toPreflight(t, p)
	view := screenText(p)
	if !strings.Contains(view, "fix: docker context use default") || strings.Contains(view, "fix: overlay2") {
		t.Fatalf("first warning focused:\n%s", view)
	}
	if !strings.Contains(rowWith(t, view, "colima"), "›") {
		t.Error("the focused row has no focus mark")
	}
	press(p, "down")
	view = screenText(p)
	if strings.Contains(view, "fix: docker context use default") || !strings.Contains(view, "fix: overlay2 is recommended for kind") {
		t.Fatalf("after Down:\n%s", view)
	}
}

// Recheck runs the checks again and shows only the new run: nothing of the old one
// survives it.
func TestRecheckReplacesTheResults(t *testing.T) {
	r := runnerOf(preCheck(installflow.CheckFail, "port 80 in use by nginx", "stop what holds port 80 and re-run"))
	p := newTestPrerun(&Wizard{Facts: preFacts()}, r)
	toPreflight(t, p)
	*r = *runnerOf(preCheck(installflow.CheckPass, "port 80 free", ""))
	tabTo(t, p, actRecheck)
	press(p, "enter")
	view := screenText(p)
	if r.calls.Load() != 1 || strings.Contains(view, "nginx") || !strings.Contains(view, "port 80 free") {
		t.Fatalf("after Recheck (%d runs):\n%s", r.calls.Load(), view)
	}
	if !p.canContinue() {
		t.Fatal("a clean recheck left Continue unavailable")
	}
}

// A message from a run that has been replaced is dropped.
func TestAStaleRunIsNotShown(t *testing.T) {
	p := newTestPrerun(&Wizard{Facts: preFacts()}, runnerOf(preCheck(installflow.CheckPass, "kubectl", "")))
	toPreflight(t, p)
	stale := preCheck(installflow.CheckFail, "from an older run", "x")
	p.update(checkMsg{gen: p.pf.gen - 1, obs: &stale})
	p.update(checkMsg{gen: p.pf.gen - 1, done: true, res: installflow.PreflightResult{Fails: 1}})
	if strings.Contains(screenText(p), "older run") || !p.canContinue() {
		t.Fatal("a replaced run's results reached the screen")
	}
}

// While the checks run, nothing that depends on them can be chosen.
func TestNothingIsChosenWhileTheChecksRun(t *testing.T) {
	block := make(chan struct{})
	r := installflow.PreflightFunc(func(_ context.Context, observe func(installflow.PreflightObservation)) (installflow.PreflightResult, error) {
		observe(preCheck(installflow.CheckPass, "kubectl", ""))
		<-block
		return installflow.PreflightResult{}, nil
	})
	p := newTestPrerun(&Wizard{Facts: preFacts(), Replan: func(bool) (installflow.Engine, installflow.PreflightRunner, error) {
		t.Fatal("replanned while the checks ran")
		return nil, nil, nil
	}}, r)
	cmd, _ := p.update(keyOf("enter"))
	cmd, _ = p.update(cmd()) // the first observation
	if !p.pf.running || p.canContinue() {
		t.Fatal("Continue is available while the checks run")
	}
	if v := screenText(p); !strings.Contains(v, preflightRunning) {
		t.Errorf("the running state is not said:\n%s", v)
	}
	for _, k := range []string{"enter", "tab", "enter", "tab", "enter", "tab", "enter"} {
		if out := press(p, k); out == proceed || p.pf.confirm {
			t.Fatalf("%q acted while the checks ran", k)
		}
	}
	close(block)
	pump(p, cmd)
	if p.pf.running || !p.canContinue() {
		t.Fatal("the checks did not finish")
	}
}

func TestARunnerErrorBlocksContinueAndIsRedacted(t *testing.T) {
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "canary-s3cr3t-value-7731"
	if err := hub.AddSecret(canary); err != nil {
		t.Fatal(err)
	}
	r := &preRunner{
		obs: []installflow.PreflightObservation{preCheck(installflow.CheckWarn, "token "+canary+" \x1b]0;title\x07rejected",
			"rotate "+canary)},
		err: errors.New("kubeconfig holds " + canary),
	}
	p := newPrerun(context.Background(), preCaps(terminal.L4, 100, 30),
		&Wizard{Facts: Facts{Version: "dcctl \x1b[31mv1", KubeContext: "ctx-" + canary}}, r, hub.Redact)
	toPreflight(t, p)
	raw := p.render()
	press(p, "esc")
	raw += p.render()
	if strings.Contains(raw, canary) {
		t.Fatalf("a secret reached the screen:\n%s", raw)
	}
	if strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\x1b[31m") {
		t.Fatalf("an escape from a check reached the screen: %q", raw)
	}
	p.screen = screenPreflight
	if p.canContinue() || !strings.Contains(screenText(p), "Preflight could not finish") {
		t.Fatalf("a runner error left Continue available:\n%s", screenText(p))
	}
}

func TestNoRunnerBlocksContinue(t *testing.T) {
	p := newTestPrerun(&Wizard{Facts: preFacts()}, nil)
	toPreflight(t, p)
	if p.canContinue() || !strings.Contains(screenText(p), preflightNoRunner) {
		t.Fatalf("no runner:\n%s", screenText(p))
	}
}

func TestSkipPreflightIsNotChecked(t *testing.T) {
	f := preFacts()
	f.SkipPreflight = true
	r := runnerOf(preCheck(installflow.CheckFail, "should not run", "x"))
	p := newTestPrerun(&Wizard{Facts: f}, r)
	toPreflight(t, p)
	view := screenText(p)
	if r.calls.Load() != 0 || !strings.Contains(view, "NOT CHECKED") || !strings.Contains(view, "--skip-preflight supplied") {
		t.Fatalf("skip-preflight (%d runs):\n%s", r.calls.Load(), view)
	}
	if strings.Contains(view, "Recheck") || !p.canContinue() {
		t.Fatalf("skip-preflight offers a recheck or blocks:\n%s", view)
	}
}

// Change footprint shows what the footprint means first, changes the plan through
// Replan, and runs every check again for the new plan.
func TestSwitchingTheFootprintReplansAndRechecks(t *testing.T) {
	full := runnerOf(preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix))
	compactRunner := runnerOf(preCheck(installflow.CheckWarn, "engine memory: 6.0Gi", capacityFix))
	compactEngine := engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
		return installflow.Summary{}, nil
	})
	var asked []bool
	p := newTestPrerun(&Wizard{Facts: preFacts(), Replan: func(c bool) (installflow.Engine, installflow.PreflightRunner, error) {
		asked = append(asked, c)
		return compactEngine, compactRunner, nil
	}}, full)
	toPreflight(t, p)
	tabTo(t, p, actFootprint)
	if !strings.Contains(screenText(p), "Switch to Compact") {
		t.Fatalf("no footprint action:\n%s", screenText(p))
	}
	press(p, "enter")
	if !p.pf.confirm || !strings.Contains(screenText(p), "no monitoring stack") {
		t.Fatalf("the consequences are not shown before the switch:\n%s", screenText(p))
	}
	press(p, "esc")
	if p.pf.confirm || len(asked) != 0 || p.pf.compact {
		t.Fatal("Esc changed the plan")
	}
	press(p, "enter", "enter")
	if len(asked) != 1 || !asked[0] || !p.pf.compact {
		t.Fatalf("replan asked %v, compact %v", asked, p.pf.compact)
	}
	if compactRunner.calls.Load() != 1 || full.calls.Load() != 1 {
		t.Fatalf("checks after the switch: full %d, compact %d", full.calls.Load(), compactRunner.calls.Load())
	}
	view := screenText(p)
	if !strings.Contains(view, "footprint Compact") || !strings.Contains(rowWith(t, view, "engine memory"), "WARN") {
		t.Fatalf("after the switch:\n%s", view)
	}
	if !p.canContinue() || p.engine == nil {
		t.Fatal("the switched plan is not what continues")
	}
	press(p, "esc")
	if v := screenText(p); !strings.Contains(v, "Compact (changed on the preflight screen)") {
		t.Errorf("the welcome screen does not show the changed footprint:\n%s", v)
	}
}

func TestARefusedFootprintChangeKeepsThePlan(t *testing.T) {
	full := runnerOf(preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix))
	p := newTestPrerun(&Wizard{Facts: preFacts(), Replan: func(bool) (installflow.Engine, installflow.PreflightRunner, error) {
		return nil, nil, errors.New("--compact cannot be combined with --size base")
	}}, full)
	toPreflight(t, p)
	tabTo(t, p, actFootprint)
	press(p, "enter", "enter")
	view := screenText(p)
	if p.pf.compact || p.engine != nil || full.calls.Load() != 1 || !strings.Contains(view, "cannot be combined") {
		t.Fatalf("a refused change:\n%s", view)
	}
	if p.canContinue() {
		t.Fatal("a refused change unblocked Continue")
	}
}

func TestATerminalTooSmallTakesNoInput(t *testing.T) {
	p := newTestPrerun(&Wizard{Facts: preFacts()}, runnerOf())
	p.resize(59, 30)
	if out := press(p, "enter"); out != stay || p.screen != screenWelcome {
		t.Fatal("a key acted on a screen too small to show")
	}
	if v := screenText(p); !strings.Contains(v, "--ui=plain") || !strings.Contains(v, "60x16") {
		t.Fatalf("too small:\n%s", v)
	}
	p.resize(80, 24)
	if press(p, "enter"); p.screen != screenPreflight {
		t.Fatal("the screen did not come back after a resize")
	}
}

func TestWelcomeStatesTheScopeAndThePlan(t *testing.T) {
	f := preFacts()
	f.KubeContext, f.Cluster, f.Compact, f.FootprintFlag, f.HA, f.TopologyFlag = "prod", "", true, "--compact", true, "--ha"
	p := newTestPrerun(&Wizard{Facts: f}, nil)
	view := screenText(p)
	for _, want := range []string{welcomeScope, welcomeNoChanges, "existing cluster, context prod",
		"Compact (from --compact)", "HA (from --ha)", "published images v1.12.0", "local"} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), want) {
			t.Errorf("the welcome screen does not say %q:\n%s", want, view)
		}
	}
	if out := press(p, "q", "enter"); out != leave {
		t.Fatalf("q then Enter: %v", out)
	}
}

// ---- through the presenter -------------------------------------------------------

// wizardHarness is a presenter with the screens before the run, on L1 at 100x30.
func wizardHarness(t *testing.T, w *Wizard) *harness {
	caps := tuiCaps(false)
	caps.Width, caps.Height, caps.SizeKnown, caps.Layout = 100, 30, true, terminal.Full
	h := newHarness(t, caps)
	h.p.Wizard = w
	return h
}

func (h *harness) runWith(e installflow.Engine, pf installflow.PreflightRunner) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- h.p.Run(context.Background(), e, installflow.Request{Operation: installflow.OperationInstall}, pf)
	}()
	return done
}

// The run starts only after Continue, and with the plan the screens settled on: here, the
// engine a footprint switch replanned to, whose own preflight is the one it runs.
func TestTheRunStartsWithThePlanTheScreensSettled(t *testing.T) {
	var started, original atomic.Bool
	var enginePreflight atomic.Int32
	full := runnerOf(preCheck(installflow.CheckFail, "engine memory: 6.0Gi", capacityFix))
	compactRunner := runnerOf(preCheck(installflow.CheckWarn, "engine memory: 6.0Gi", capacityFix))
	compactEngine := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		started.Store(true)
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		before := compactRunner.calls.Load()
		if _, err := rt.Preflight.RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
			return installflow.Summary{}, err
		}
		enginePreflight.Store(compactRunner.calls.Load() - before)
		return succeeded, endRun(rt)
	})
	h := wizardHarness(t, &Wizard{Facts: preFacts(), Replan: func(bool) (installflow.Engine, installflow.PreflightRunner, error) {
		return compactEngine, compactRunner, nil
	}})
	done := h.runWith(engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
		original.Store(true)
		return succeeded, nil
	}), full)
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("\r")
	waitFor(t, "the failure", h.out, screenShows(h.out, "Continue (unavailable)"))
	if started.Load() || original.Load() {
		t.Fatal("the run started before Continue")
	}
	h.press("\t\r") // Recheck is focused; Tab to the footprint switch
	waitFor(t, "the consequences", h.out, screenShows(h.out, "Switch to Compact?"))
	h.press("\r")
	waitFor(t, "the compact tally", h.out, screenShows(h.out, "ready, with 1 warning"))
	h.press("\x1b[Z\r") // Shift+Tab from Recheck back to Continue
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	h.press("\r")
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if original.Load() || !started.Load() || enginePreflight.Load() != 1 {
		t.Fatalf("original engine ran %v, replanned %v, its preflight ran %d times",
			original.Load(), started.Load(), enginePreflight.Load())
	}
}

// Exit before the run changes nothing: the run settles at once as cancelled (no
// preflight runs under it), the program ends without a key, and the result is not a
// success.
func TestExitBeforeTheRunSettlesAtOnce(t *testing.T) {
	r := runnerOf(preCheck(installflow.CheckPass, "kubectl", ""))
	h := wizardHarness(t, &Wizard{Facts: preFacts()})
	var mu sync.Mutex
	var ranCluster bool
	done := h.runWith(engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		if _, err := rt.Preflight.RunPreflight(ctx, func(installflow.PreflightObservation) {}); err != nil {
			return cancelled, err
		}
		mu.Lock()
		ranCluster = true
		mu.Unlock()
		return succeeded, endRun(rt)
	}), r)
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("q\r")
	err := h.result(done)
	if err == nil {
		t.Fatal("Exit returned success")
	}
	mu.Lock()
	defer mu.Unlock()
	if ranCluster || r.calls.Load() != 0 {
		t.Fatalf("Exit ran the install (%v) or its preflight (%d)", ranCluster, r.calls.Load())
	}
	if !strings.Contains(h.out.String(), "dcctl install: cancelled") {
		t.Errorf("exit summary:\n%q", h.out.String())
	}
}

// --yes is progress-only: no welcome, no preflight screen, even with a wizard given.
func TestAProgressOnlyRunSkipsTheScreens(t *testing.T) {
	h := wizardHarness(t, &Wizard{Facts: preFacts()})
	h.p.Caps.ProgressOnly = true
	done := h.runWith(engineFunc(func(_ context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		return succeeded, endRun(rt)
	}), nil)
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.out.String(), welcomeNoChanges) {
		t.Fatal("a progress-only run showed the welcome screen")
	}
}

// ---- the splash in front of the screens -----------------------------------------

func TestTheSplashIsTheAnimatedModelWhenEligible(t *testing.T) {
	for _, c := range []struct {
		name    string
		caps    terminal.Capabilities
		animate bool
		art     terminal.Art
	}{
		{"truecolor 120x40", splashCaps(120, 40), true, terminal.LargeMark},
		{"ANSI16 is a one-line header", func() terminal.Capabilities {
			c := splashCaps(120, 40)
			c.Level, c.Splash, c.Animate = terminal.L2, terminal.TextHeader, false
			return c
		}(), false, terminal.TextHeader},
	} {
		m := newModel(c.caps, &interrupter{}, func() {}, nil, nil, modelOptions{})
		if m.splashM == nil {
			t.Fatalf("%s: no splash", c.name)
		}
		if s := m.splashM.Splash(); s.Animating() != c.animate || s.Art() != c.art {
			t.Errorf("%s: animating %v art %v", c.name, s.Animating(), s.Art())
		}
	}
	if m := newModel(tuiCaps(false), &interrupter{}, func() {}, nil, nil, modelOptions{}); m.splashM != nil || m.splash {
		t.Error("a session with no art got a splash")
	}
}

// The key that skips the splash is consumed: it neither starts the run nor acts on the
// welcome screen that follows.
func TestTheSplashSkipKeyNeverReachesTheWelcomeScreen(t *testing.T) {
	var starts atomic.Int32
	pre := newTestPrerun(&Wizard{Facts: preFacts()}, runnerOf())
	m := newModel(splashCaps(120, 40), &interrupter{}, func() { starts.Add(1) }, nil, nil, modelOptions{pre: pre})
	m.splashM.tick = func(time.Duration) tea.Cmd { return nil }
	m.Init()
	next, cmd := m.Update(keyOf("enter"))
	m = next.(model)
	for _, msg := range messages(cmd) {
		next, _ = m.Update(msg)
		m = next.(model)
	}
	if m.splash || !m.prePhase || pre.screen != screenWelcome || starts.Load() != 0 {
		t.Fatalf("after skipping: splash %v prePhase %v screen %v starts %d", m.splash, m.prePhase, pre.screen, starts.Load())
	}
	if !strings.Contains(preStrip(m.render()), welcomeNoChanges) {
		t.Fatal("the welcome screen is not up after the splash")
	}
}
