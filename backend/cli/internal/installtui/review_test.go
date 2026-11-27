// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The review settles at once in these tests, so that pump is not held up by it. A test
// about the settle itself delivers (or withholds) the settle message by hand.
func init() {
	reviewSettle = 0
	preScenarios = append(preScenarios, reviewScenarios...)
}

// goldenReview is the review of the plan f, reached through a passing preflight.
func goldenReview(c terminal.Capabilities, f Facts, rv Review, cmd result.Command) *prerun {
	p := goldenChoices(c, f, false)
	p.review, p.command = rv, cmd
	for p.focused() != chContinue {
		press(p, "tab")
	}
	press(p, "enter")
	return p
}

var reviewScenarios = []struct {
	name string
	make func(caps terminal.Capabilities) *prerun
}{
	{"review-kind", func(c terminal.Capabilities) *prerun {
		return goldenReview(c, preFacts(), testReview(preFacts()), testCommand(preFacts()))
	}},
	{"review-context-template", func(c terminal.Capabilities) *prerun {
		f := goldenContextFacts()
		rv := testReview(f)
		rv.Warnings = []string{"no database backups: a lost volume cannot be recovered",
			"--skip-preflight: the read-only checks are not run, here or by the install"}
		cmd := testCommand(f)
		args := append(append([]string{}, cmd.Args[:len(cmd.Args)-1]...),
			"'--alert-webhook=<redacted: supply privately>'", "'--yes=true'")
		p := goldenReview(c, f, rv, result.Command{Line: strings.Join(args, " "), Args: args, Template: true})
		press(p, "tab")
		return p
	}},
	{"review-settling", func(c terminal.Capabilities) *prerun {
		p := goldenChoices(c, preFacts(), false)
		p.review, p.command = testReview(preFacts()), testCommand(preFacts())
		for p.focused() != chContinue {
			press(p, "tab")
		}
		p.update(keyOf("enter")) // its settle withheld
		return p
	}},
}

// withSettle makes the review take d to settle for one test.
func withSettle(t *testing.T, d time.Duration) {
	t.Helper()
	old := reviewSettle
	reviewSettle = d
	t.Cleanup(func() { reviewSettle = old })
}

// testReview is a review as a command would build it from the plan f.
func testReview(f Facts) Review {
	target := "local kind cluster " + f.Cluster + ": created if it does not exist, reused if it does"
	if f.KubeContext != "" {
		target = "existing cluster, context " + f.KubeContext + ": reused as it is; dcctl never creates or deletes it"
	}
	shape := footprintWord(f.Compact) + " footprint, " + topologyWord(f.HA)
	return Review{Rows: []ReviewRow{
		{"Target", target},
		{"Release", "dcctl v1.12.0; " + f.Images},
		{"Shape", shape},
		{"TLS", "on (cert-manager)"},
		{"Backups", "on, to a store the cluster provisions for itself (no off-site archive)"},
		{"State", "/home/op/.devicechain/clusters/<assigned after identification>/infra"},
	}}
}

// testCommand is the equivalent command a command would serialise for the plan f.
func testCommand(f Facts) result.Command {
	args := []string{"'dcctl'", "'install'", "'local'"}
	if f.Compact {
		args = append(args, "'--compact=true'")
	}
	if f.HA {
		args = append(args, "'--ha=true'")
	}
	if f.KubeContext != "" {
		args = append(args, "'--kube-context="+f.KubeContext+"'")
	}
	args = append(args, "'--yes=true'")
	return result.Command{Line: strings.Join(args, " "), Args: args}
}

// toReview goes from the welcome screen to the review, and lets it settle.
func toReview(t *testing.T, p *prerun) {
	t.Helper()
	toChoices(t, p)
	focusOn(t, p, chContinue)
	if out := press(p, "enter"); out != stay || p.screen != screenReview {
		t.Fatalf("Continue on the choices: %v, screen %v:\n%s", out, p.screen, screenText(p))
	}
	if !p.rv.armed {
		t.Fatal("the review did not settle")
	}
}

func reviewPrerun() (*prerun, *replanner) {
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	return newTestPrerun(choicesWizard(r), passing()), r
}

// 🔴 CONTINUE ON THE CHOICES NEVER STARTS THE RUN. It shows the review, focused on
// Back, and nothing is approved.
func TestContinueLeadsToTheReviewNeverToTheRun(t *testing.T) {
	p, _ := reviewPrerun()
	toChoices(t, p)
	focusOn(t, p, chContinue)
	cmd, out := p.update(keyOf("enter"))
	if out != stay || p.screen != screenReview || cmd == nil {
		t.Fatalf("Continue gave %v on screen %v (settle %v)", out, p.screen, cmd != nil)
	}
	if p.rv.focus != reviewBack || p.approval != nil {
		t.Fatalf("the review opened focused on %d, approval %v", p.rv.focus, p.approval)
	}
	pump(p, cmd)
	view := screenText(p)
	for _, want := range []string{reviewTitle, "local kind cluster devicechain: created if it does not exist",
		"Standard footprint, Single", "<assigned after identification>", reviewCommandTitle,
		"'dcctl' 'install' 'local' '--yes=true'", "Preflight: all checks passed.", "kind cluster devicechain exists yet"} {
		if !strings.Contains(view, want) {
			t.Errorf("the review does not say %q:\n%s", want, view)
		}
	}
	if !strings.Contains(rowWith(t, view, reviewApproveLabel), focusGlyph(p)+" Back") {
		t.Errorf("Back is not focused:\n%s", view)
	}
	// An Enter on the settled review, with focus where it opened, goes back.
	if out := press(p, "enter"); out != stay || p.screen != screenChoices || p.approval != nil {
		t.Fatalf("Enter on the review gave %v on screen %v", out, p.screen)
	}
}

func focusGlyph(p *prerun) string { return p.theme.Glyphs.Focus }

// 🔴 A KEY TYPED BEFORE THE REVIEW SETTLED NEVER APPROVES IT: not a queued Enter, not
// a queued Tab then Enter, and not a settle left over from an earlier visit.
func TestAQueuedKeyNeverApproves(t *testing.T) {
	p, _ := reviewPrerun()
	toChoices(t, p)
	focusOn(t, p, chContinue)
	first, _ := p.update(keyOf("enter")) // the settle for this visit is withheld
	for _, keys := range [][]string{{"enter"}, {"tab", "enter"}, {"shift+tab", "enter"}, {"tab", "tab", "enter"},
		{"q", "enter"}, {"esc"}} {
		for _, k := range keys {
			if _, out := p.update(keyOf(k)); out != stay {
				t.Fatalf("queued %v gave %v", keys, out)
			}
		}
		if p.screen != screenReview || p.rv.focus != reviewBack || p.approval != nil {
			t.Fatalf("queued %v acted: screen %v focus %d approval %v", keys, p.screen, p.rv.focus, p.approval)
		}
	}
	if v := screenText(p); !strings.Contains(v, reviewApprovalPending) || !strings.Contains(v, reviewApproveLabel+" (unavailable)") {
		t.Fatalf("an unsettled review does not say so:\n%s", v)
	}
	// The first visit settles, then is left; the second visit is not settled by the
	// first visit's message arriving again.
	pump(p, first)
	press(p, "esc")
	_, _ = p.update(keyOf("enter")) // Continue again: a new visit, its settle withheld
	if p.screen != screenReview || p.rv.armed {
		t.Fatal("the second visit is settled before its own settle")
	}
	p.update(checkMsg{settled: p.rv.gen - 1})
	for _, k := range []string{"tab", "enter"} {
		if _, out := p.update(keyOf(k)); out != stay {
			t.Fatalf("a key after an earlier visit's settle gave %v", out)
		}
	}
	if p.rv.armed || p.approval != nil {
		t.Fatal("an earlier visit's settle armed this one")
	}
}

// The run starts from an explicit approval, and only then: Tab to Approve, then Enter.
// The approval is for exactly this plan's kind cluster.
func TestApprovalIsExplicitAndForThisPlan(t *testing.T) {
	p, _ := reviewPrerun()
	toReview(t, p)
	press(p, "tab")
	if p.rv.focus != reviewApprove {
		t.Fatalf("Tab focused %d", p.rv.focus)
	}
	if out := press(p, "enter"); out != proceed {
		t.Fatalf("Approve gave %v", out)
	}
	if c, ok := p.approvedKindCluster(); !ok || c != "devicechain" {
		t.Fatalf("approved %q %v", c, ok)
	}
}

// An existing context asks no creation question: the approval answers none.
func TestApprovingAContextAnswersNoQuestion(t *testing.T) {
	f := preFacts()
	f.KubeContext, f.Cluster = "prod", ""
	r := &replanner{base: &f, runner: func(Edits) installflow.PreflightRunner { return passing() }}
	w := choicesWizard(r)
	w.Facts, w.Review, w.Command = f, testReview(f), testCommand(f)
	p := newTestPrerun(w, passing())
	toReview(t, p)
	if v := screenText(p); strings.Contains(v, "kind cluster") || !strings.Contains(v, "--kube-context=prod") {
		t.Fatalf("context review:\n%s", v)
	}
	press(p, "tab")
	if out := press(p, "enter"); out != proceed {
		t.Fatalf("Approve gave %v", out)
	}
	if c, ok := p.approvedKindCluster(); ok {
		t.Fatalf("a context approval answers for kind cluster %q", c)
	}
}

// 🔴 ANY CHANGE AFTER THE REVIEW INVALIDATES IT. Back, then an edit: the plan is new,
// its checks start again, Approve is unavailable until they pass, the review it gets is
// its own, and an approval given on the old plan answers nothing.
func TestAChangeAfterReviewInvalidatesTheApprovalAndThePreflight(t *testing.T) {
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
	toReview(t, p)
	press(p, "tab", "enter") // approved, as the old plan stood
	if _, ok := p.approvedKindCluster(); !ok {
		t.Fatal("no approval to invalidate")
	}
	p.screen = screenReview
	press(p, "esc")
	if p.screen != screenChoices || p.approval != nil {
		t.Fatal("Back kept the approval")
	}
	// An approval held over from the old plan, however it survived, answers nothing.
	held := &approval{plan: p.plan, cluster: "devicechain"}
	focusOn(t, p, chFootprint)
	cmd, _ := p.update(keyOf("space"))
	p.approval = held
	if _, ok := p.approvedKindCluster(); ok {
		t.Fatal("an approval of the old plan answers for the new one")
	}
	if !p.pf.running || p.canContinue() || p.canApprove() {
		t.Fatal("the new plan kept the old plan's checks")
	}
	if !strings.Contains(p.command.Line, "--compact=true") || !strings.Contains(p.review.Rows[2].Value, "Compact") {
		t.Fatalf("the review is not the new plan's: %q %+v", p.command.Line, p.review.Rows)
	}
	focusOn(t, p, chContinue)
	if out := press(p, "enter"); out != stay || p.screen != screenChoices {
		t.Fatalf("Continue reached the review before the new plan's checks: %v %v", out, p.screen)
	}
	close(block)
	pump(p, cmd)
	press(p, "enter")
	if p.screen != screenReview || p.rv.focus != reviewBack {
		t.Fatalf("the new plan's review: screen %v focus %d", p.screen, p.rv.focus)
	}
	if v := screenText(p); !strings.Contains(v, "Compact footprint") {
		t.Fatalf("the review does not show the change:\n%s", v)
	}
}

// Approve is unavailable on a review whose plan's checks no longer pass.
func TestApproveNeedsThePlansChecksToHavePassed(t *testing.T) {
	p, _ := reviewPrerun()
	toReview(t, p)
	p.pf.running = true
	press(p, "tab")
	if out := press(p, "enter"); out != stay || p.approval != nil {
		t.Fatalf("Approve with the checks running gave %v", out)
	}
	if v := screenText(p); !strings.Contains(v, reviewNotApprovable) {
		t.Fatalf("the review does not say why:\n%s", v)
	}
}

// Exit on the review leaves, changing nothing; q moves the focus to it first.
func TestExitFromTheReview(t *testing.T) {
	p, _ := reviewPrerun()
	toReview(t, p)
	if out := press(p, "q"); out != stay || p.rv.focus != reviewExit {
		t.Fatalf("q gave %v focus %d", out, p.rv.focus)
	}
	if out := press(p, "enter"); out != leave || p.approval != nil {
		t.Fatalf("Exit gave %v", out)
	}
}

// Every text the review draws is sanitised and redacted; a credential in the command is
// a placeholder, and the command is labelled a template.
func TestTheReviewDrawsOnlySafeText(t *testing.T) {
	const canary = "dcCanaryReviewSecret_Zq81"
	hub, err := installflow.NewHub(installflow.Options{Operation: installflow.OperationInstall,
		Sink: installflow.SinkFunc(func(installflow.Envelope) {})})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.AddSecret(canary); err != nil {
		t.Fatal(err)
	}
	f := preFacts()
	r := &replanner{runner: func(Edits) installflow.PreflightRunner { return passing() }}
	w := choicesWizard(r)
	w.Review = Review{Rows: []ReviewRow{{"Target", "kind \x1b[31mred\x1b[0m\u200bcluster " + canary}},
		Warnings: []string{"warn \x1b]0;title\x07" + canary}}
	args := []string{"'dcctl'", "'install'", "'local'", "'--alert-webhook=<redacted: supply privately>'",
		"'--registry=" + canary + "'", "'--yes=true'"}
	w.Command = result.Command{Line: strings.Join(args, " "), Args: args, Template: true}
	w.Facts = f
	p := newPrerun(context.Background(), preCaps(terminal.L4, 200, 40), w, passing(), hub.Redact)
	toReview(t, p)
	raw := p.render()
	view := preStrip(raw)
	if strings.Contains(raw, canary) || strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\u200b") ||
		strings.Contains(view, "\x1b") {
		t.Fatalf("the review drew unsafe text:\n%q", raw)
	}
	for _, want := range []string{reviewCommandTitle + " (template)", reviewTemplate, "<redacted: supply privately>",
		"kind redcluster", "WARN"} {
		if !strings.Contains(view, want) {
			t.Errorf("the review does not say %q:\n%s", want, view)
		}
	}
}

// The plan scrolls on a small terminal: every row can be reached, and the actions stay.
func TestTheReviewScrollsOnASmallTerminal(t *testing.T) {
	p, _ := reviewPrerun()
	p.resize(200, 40)
	toReview(t, p)
	p.resize(60, 16)
	seen := map[string]bool{}
	for range 40 {
		v := screenText(p)
		if !strings.Contains(v, "Back") || !strings.Contains(v, reviewApproveLabel) {
			t.Fatalf("the actions scrolled away:\n%s", v)
		}
		for _, want := range []string{"Target", "State", reviewCommandTitle, "'--yes=true'"} {
			seen[want] = seen[want] || strings.Contains(v, want)
		}
		press(p, "down")
	}
	for _, want := range []string{"Target", "State", reviewCommandTitle, "'--yes=true'"} {
		if !seen[want] {
			t.Errorf("%s is out of reach", want)
		}
	}
}

// The bridge answers the run's question to create the kind cluster that was approved,
// once, and nothing else.
func TestTheApprovalAnswersOnlyItsOwnQuestionOnce(t *testing.T) {
	b := &bridge{send: func(tea.Msg) {}, done: make(chan struct{})}
	ask := func(cluster string) installflow.Question {
		return installflow.Question{Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params: []installflow.Param{{Name: "cluster", Value: installflow.Text(cluster)}}}
	}
	if b.answeredByApproval(ask("devicechain")) {
		t.Fatal("answered with no approval")
	}
	b.approve("devicechain")
	other := ask("devicechain")
	other.Question = "install.something-else"
	if b.answeredByApproval(ask("other")) || b.answeredByApproval(other) {
		t.Fatal("the approval answered another question")
	}
	if !b.answeredByApproval(ask("devicechain")) {
		t.Fatal("the approval did not answer its own question")
	}
	if b.answeredByApproval(ask("devicechain")) {
		t.Fatal("the approval answered twice")
	}
}

// ---- through the presenter -------------------------------------------------------

// Through the real program: keys typed ahead of the review never approve it, the run
// starts only from Approve, and the approval answers the run's kind-creation question
// without asking it again.
func TestThroughTheProgramOnlyAnApprovalStartsTheRun(t *testing.T) {
	withSettle(t, 300*time.Millisecond)
	var started atomic.Bool
	var answered atomic.Bool
	engine := engineFunc(func(ctx context.Context, rt installflow.Runtime) (installflow.Summary, error) {
		started.Store(true)
		if err := startRun(rt); err != nil {
			return installflow.Summary{}, err
		}
		ans, err := rt.Prompts.Ask(ctx, installflow.Prompt{Step: installflow.StepInstallCluster,
			Kind: installflow.PromptConfirm, Question: installflow.QuestionCreateKindCluster,
			Params:        []installflow.Param{{Name: "cluster", Value: installflow.Text("devicechain")}},
			YesResolvable: true})
		if err != nil || !ans.Confirmed {
			return installflow.Summary{}, err
		}
		answered.Store(true)
		return succeeded, endRun(rt)
	})
	w := &Wizard{Facts: preFacts(), Review: testReview(preFacts()), Command: testCommand(preFacts())}
	h := wizardHarness(t, w)
	done := h.runWith(engine, runnerOf(preCheck(installflow.CheckPass, "kubectl", "")))
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("\r")
	waitFor(t, "the tally", h.out, screenShows(h.out, "all checks passed"))
	h.press("\x1b[Z\r") // Shift+Tab from Recheck to Continue
	waitFor(t, "the cluster choices", h.out, screenShows(h.out, choicesTitle))
	// Continue, and typed ahead with it: Tab and Enter, which would approve.
	h.press("\r\t\r")
	waitFor(t, "the review", h.out, screenShows(h.out, reviewCommandTitle))
	time.Sleep(reviewSettle + 300*time.Millisecond)
	if started.Load() {
		t.Fatal("keys typed before the review settled started the run")
	}
	h.press("\t\r")
	waitFor(t, "the verdict", h.out, screenShows(h.out, "Install succeeded."))
	if strings.Contains(h.out.String(), "Create it now?") {
		t.Error("the approved question was asked again")
	}
	h.press("\r")
	if err := h.result(done); err != nil {
		t.Fatal(err)
	}
	if !started.Load() || !answered.Load() {
		t.Fatalf("started %v answered %v", started.Load(), answered.Load())
	}
}

// A command with a word too wide for the screen is never drawn split: the review says
// so, and the exact command is printed when the installer exits.
func TestATooWideCommandIsNotDrawnSplit(t *testing.T) {
	p, _ := reviewPrerun()
	long := "'--alert-config=/home/op/" + strings.Repeat("x", 120) + ".yaml'"
	args := []string{"'dcctl'", "'install'", "'local'", long, "'--yes=true'"}
	p.command = result.Command{Line: strings.Join(args, " "), Args: args}
	toReview(t, p)
	p.resize(80, 40)
	view := screenText(p)
	if strings.Contains(view, "xxxxxxxx") || !strings.Contains(strings.Join(strings.Fields(view), " "),
		"too narrow to show the equivalent command exactly") {
		t.Fatalf("a too-wide command:\n%s", view)
	}
	if got := p.exitCommand(); got != strings.Join(args, " ") {
		t.Fatalf("exit command %q", got)
	}
}

// The request's credentials are registered with the hub before any screen draws: a
// value that turns up in any text the screens show is redacted.
func TestTheWizardsSecretsAreRedactedFromEveryScreen(t *testing.T) {
	const canary = "dcCanaryWizardSecret_x81Lq"
	f := preFacts()
	f.Images = "published images v1.12.0 from https://op:" + canary + "@registry.example.test"
	w := &Wizard{Facts: f, Review: testReview(f), Command: testCommand(f), Secrets: []string{canary}}
	h := wizardHarness(t, w)
	done := h.runWith(engineFunc(func(context.Context, installflow.Runtime) (installflow.Summary, error) {
		return succeeded, nil
	}), runnerOf(preCheck(installflow.CheckPass, "kubectl", "")))
	waitFor(t, "the welcome screen", h.out, screenShows(h.out, welcomeNoChanges))
	h.press("q\r")
	_ = h.result(done)
	if strings.Contains(h.out.String(), canary) || !strings.Contains(h.out.String(), "registry.example.test") {
		t.Fatalf("the secret was drawn, or the screen did not show the row:\n%q", h.out.String())
	}
}
