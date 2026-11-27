// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// BootstrapScreens is an instance journey a Presenter runs in place of the install's
// screens: the hand-over from the cluster journey, the instance and escrow choices, the
// instance review and its approval, then the run, and after a successful run the
// escrow receipt, the first login and the instance success.
//
// 🔴 NOTHING OF THE INSTANCE RUNS WITHOUT ITS OWN APPROVAL. The run starts only from
// Approve on the instance review, once that screen has settled; the hand-over's Continue
// and every choice before it start nothing. A presenter is made for one run, so an
// approval given in an earlier program (the cluster's review) is never in this one's
// hands, and cannot answer anything this run asks.
type BootstrapScreens struct {
	// Setup is what the instance screens start from. The presenter sets its Caps and
	// Reveals.
	Setup BootstrapSetup
	// Handover, when set, is shown first.
	Handover *Handover
	// Plan builds the run the settled choices describe, through the path the bootstrap
	// command line itself takes. An error is shown on the review, where Approve is then
	// unavailable.
	Plan func(BootstrapPlanned) (BootstrapPlan, error)
	// Facts is what the success screens state, read back once the run has finished.
	Facts func(installflow.Summary) InstanceFacts
	// Chosen, when set, is told each time the choices settle (the review is entered),
	// so a caller can name the command that builds this instance later.
	Chosen func(BootstrapPlanned)
}

// Handover is the screen between the cluster journey and the instance journey.
type Handover struct {
	// Target names the cluster that was installed, as the screen states it.
	Target string
	// Continue is the command that creates an instance on it later, by hand.
	Continue string
}

// BootstrapPlan is the run a set of instance choices describes.
type BootstrapPlan struct {
	Engine    installflow.Engine
	Preflight installflow.PreflightRunner
	// Review is the plan as the instance review states it.
	Review Review
	// Command is the equivalent non-interactive command, as a template.
	Command string
}

// The instance journey's stages, before and after its run.
type instStage uint8

const (
	instHandover instStage = iota
	instChoices
	instReview
	instRun
	instAfter
)

// The hand-over's actions.
const (
	handoverExit = iota
	handoverContinue
)

// Words of the hand-over and the instance review.
const (
	handoverTitle = "Cluster installed"
	handoverIntro = "The shared cluster prerequisites are installed. No instance has been created yet."
	handoverNext  = "Next: create an instance on this cluster. It is a separate step, with its own choices, " +
		"its own review and its own approval; nothing of it runs until you approve it."
	handoverStay = "If you exit here, the cluster stays installed. To create an instance later, run:"

	instReviewTitle = "Review the instance"
	instReviewIntro = "This is the instance the bootstrap will build, as the command line would resolve it. " +
		"Nothing of it has been created yet. Approve starts it; Back returns to the choices."
	instReviewPending = "Approve becomes available once this screen has settled; a key pressed before then does nothing."
	instReviewBlocked = "Approve is unavailable: this plan could not be built. Go back and change it."
)

// instancePhase is the instance journey's screens around the run.
type instancePhase struct {
	scr   *BootstrapScreens
	flow  *BootstrapFlow
	theme bsTheme
	stage instStage
	// gate arms each visit to the hand-over and the review: only once the visit and the
	// keyboard have both been quiet for the settle period (see settleGate).
	gate  settleGate
	focus int
	// plan is the run the review shows; planErr is why there is none.
	plan    *BootstrapPlan
	planErr string
}

func newInstancePhase(scr *BootstrapScreens, setup BootstrapSetup) *instancePhase {
	p := &instancePhase{scr: scr, flow: NewBootstrapFlow(setup), theme: newBSTheme(setup.Caps)}
	p.theme.width, p.theme.height = setup.Caps.Width, setup.Caps.Height
	p.stage = instChoices
	if scr.Handover != nil {
		p.stage = instHandover
	}
	return p
}

// start is the first screen's command: the hand-over settles before it takes a key.
func (p *instancePhase) start() tea.Cmd {
	if p.stage == instHandover {
		return p.visit(instHandover, handoverContinue)
	}
	return nil
}

// visit shows the hand-over or the review afresh: unarmed, with focus on def, and
// taking no key until it has settled (see settleGate).
func (p *instancePhase) visit(stage instStage, def int) tea.Cmd {
	p.stage, p.focus = stage, def
	return p.gate.enter()
}

func (p *instancePhase) resize(w, h int) {
	p.theme.width, p.theme.height = w, h
	p.flow.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// canApprove is the one gate on the instance's Approve: the review has settled, and a
// run was built for the choices on it.
func (p *instancePhase) canApprove() bool {
	return p.stage == instReview && p.gate.armed && p.plan != nil && p.plan.Engine != nil
}

// update takes a message while the screens before the run are up. Only a key can
// decide anything: proceed is Approve on a settled review, leave is Exit.
func (p *instancePhase) update(msg tea.Msg) (tea.Cmd, prerunOutcome) {
	switch msg := msg.(type) {
	case settleMsg:
		if p.stage == instHandover || p.stage == instReview {
			return p.gate.settled(msg), stay
		}
		return nil, stay
	case ScreenBack:
		if p.flow.phase == phaseInstance && p.scr.Handover != nil {
			return p.visit(instHandover, handoverContinue), stay
		}
		_, cmd := p.flow.Update(msg)
		return cmd, stay
	case BootstrapPlanned:
		return p.review(msg), stay
	case tea.KeyPressMsg:
		switch p.stage {
		case instHandover:
			return p.handoverKey(msg)
		case instReview:
			return p.reviewKey(msg)
		}
	}
	if p.stage == instChoices {
		_, cmd := p.flow.Update(msg)
		return cmd, stay
	}
	return nil, stay
}

// review builds the run for the settled choices and shows it, focused on Back.
func (p *instancePhase) review(planned BootstrapPlanned) tea.Cmd {
	if p.scr.Chosen != nil {
		p.scr.Chosen(planned)
	}
	p.plan, p.planErr = nil, ""
	if p.scr.Plan == nil {
		p.planErr = "no run can be built here"
	} else if plan, err := p.scr.Plan(planned); err != nil {
		p.planErr = err.Error()
	} else {
		p.plan = &plan
	}
	return p.visit(instReview, reviewBack)
}

func (p *instancePhase) handoverKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	if !p.shown() {
		return nil, stay // a screen the terminal cannot show whole takes no key
	}
	if ok, cmd := p.gate.admit(); !ok {
		return cmd, stay // typed before the screen settled: dropped, whatever it was
	}
	switch k.String() {
	case "tab", "shift+tab", "left", "right", "up", "down":
		p.focus = 1 - p.focus
	case "q":
		p.focus = handoverExit
	case "enter":
		if p.focus == handoverExit {
			return nil, leave
		}
		p.gate.leave()
		p.stage = instChoices
	}
	return nil, stay
}

func (p *instancePhase) reviewKey(k tea.KeyPressMsg) (tea.Cmd, prerunOutcome) {
	if !p.shown() {
		return nil, stay // a screen the terminal cannot show whole takes no key
	}
	if ok, cmd := p.gate.admit(); !ok {
		return cmd, stay // typed before the screen settled: dropped, whatever it was
	}
	switch k.String() {
	case "tab", "right", "down":
		p.focus = (p.focus + 1) % 3
	case "shift+tab", "left", "up":
		p.focus = (p.focus + 2) % 3
	case "q":
		p.focus = reviewExit
	case "esc":
		p.back()
	case "enter":
		switch p.focus {
		case reviewBack:
			p.back()
		case reviewApprove:
			if p.canApprove() {
				p.stage = instRun
				return nil, proceed
			}
		case reviewExit:
			return nil, leave
		}
	}
	return nil, stay
}

// back returns from the review to the escrow choice, which is where the choices were
// settled. Nothing is approved; the next review is a new visit that settles again.
func (p *instancePhase) back() {
	p.gate.leave()
	p.stage, p.plan, p.planErr = instChoices, nil, ""
	p.flow.phase = phaseEscrow
}

// shown reports whether the screen on the terminal is there whole: only then may a key
// act on it, as everywhere in the instance journey.
func (p *instancePhase) shown() bool { return p.theme.fits(p.flow.header(), p.screen()) }

// screen is the hand-over or the review. While it is settling it also says so, but
// only where that note fits: the note must never be what makes the screen too large to
// show, since a screen that is not shown never settles.
func (p *instancePhase) screen() bsScreen {
	build := p.reviewScreen
	if p.stage == instHandover {
		build = p.handoverScreen
	}
	if !p.gate.armed {
		if s := build(true); p.theme.fits(p.flow.header(), s) {
			return s
		}
	}
	return build(false)
}

// render is the screen before the run.
func (p *instancePhase) render() string {
	if p.stage == instChoices {
		return p.flow.Render()
	}
	return p.theme.frame(p.flow.header(), p.screen())
}

func (p *instancePhase) buttons(labels []string) []string {
	t := p.theme
	w := t.contentWidth()
	var out []string
	for i, l := range labels {
		out = append(out, t.marked(i == p.focus, "[ "+l+" ]", w)...)
	}
	return out
}

func (p *instancePhase) handoverScreen(pending bool) bsScreen {
	t, h := p.theme, p.scr.Handover
	w := t.contentWidth()
	var body []string
	body = append(body, t.para(roleGood, "", handoverIntro, w)...)
	if h.Target != "" {
		body = append(body, t.field("Cluster", h.Target, 8, w)...)
	}
	body = append(body, "")
	body = append(body, t.para(rolePlain, "", handoverNext, w)...)
	body = append(body, "")
	body = append(body, t.para(rolePlain, "", handoverStay, w)...)
	body = append(body, t.para(roleLabel, "  ", h.Continue, w)...)
	body = append(body, "")
	body = append(body, p.buttons([]string{"Exit (the cluster stays installed)", "Continue to the instance"})...)
	if pending {
		body = append(body, "", t.paint(roleDim, "This screen takes keys once it has settled."))
	}
	return bsScreen{title: handoverTitle, body: body, keys: []key.Binding{bsKeyNext, bsKeyEnter}}
}

func (p *instancePhase) reviewScreen(pending bool) bsScreen {
	t := p.theme
	w := t.contentWidth()
	var body []string
	body = append(body, t.para(rolePlain, "", instReviewIntro, w)...)
	body = append(body, "")
	if p.plan != nil {
		for _, r := range p.plan.Review.Rows {
			body = append(body, t.field(r.Label, r.Value, 12, w)...)
		}
		if len(p.plan.Review.Warnings) > 0 {
			body = append(body, "", t.paint(roleLabel, "Warnings"))
			for _, wn := range p.plan.Review.Warnings {
				body = append(body, t.para(roleWarn, "  ", wn, w)...)
			}
		}
		if p.plan.Command != "" {
			body = append(body, "", t.paint(roleLabel, "Equivalent command (template)"))
			body = append(body, t.para(rolePlain, "  ", p.plan.Command, w)...)
		}
	} else {
		body = append(body, t.para(roleWarn, "", instReviewBlocked, w)...)
		if p.planErr != "" {
			body = append(body, t.para(rolePlain, "  ", p.planErr, w)...)
		}
	}
	body = append(body, "")
	body = append(body, p.buttons([]string{"Back", "Approve and create the instance", "Exit"})...)
	if pending {
		body = append(body, "", t.paint(roleDim, instReviewPending))
	}
	return bsScreen{title: instReviewTitle, body: body, before: true,
		keys: []key.Binding{bsKeyNext, bsKeyEnter, bsKeyBack}}
}
