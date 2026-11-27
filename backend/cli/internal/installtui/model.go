// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/brand"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// splashHold is how long the static splash stays before the next screen on its own.
const splashHold = brand.StaticHold

// splashSubtitle names the journey under the logo.
const splashSubtitle = "Prepare a Kubernetes cluster"

// tickEvery is the progress screen's redraw beat while a run is live: the clock line
// and the moving marker. It is 4 redraws a second, under the 12 the splash may use.
const tickEvery = 250 * time.Millisecond

// withheld replaces a value in a fallback line, checked by result.Input.Line.
const withheld = "[withheld]"

// Messages from outside the program.
type (
	// eventMsg is one event, delivered by the hub in seq order.
	eventMsg struct{ env installflow.Envelope }
	// promptMsg asks the operator a question. reply takes exactly one answer.
	promptMsg struct {
		q     installflow.Question
		reply chan<- installflow.Response
	}
	// stoppingMsg says the run's context is done, whatever cancelled it.
	stoppingMsg struct{}
	// tickMsg redraws the clock line and the moving marker.
	tickMsg struct{}
)

// model is the install screen: the splash, the welcome and preflight screens, then the
// progress screen (the overall bar, a row per display stage with its own bar, the clock
// and budget line, the log pane), the one open question, the stopping notice, and the
// verdict.
type model struct {
	caps terminal.Capabilities
	intr *interrupter
	// start lets the engine begin. It is idempotent.
	start func()
	logs  logSource
	// redact is the hub's redaction (Hub.Redact), applied to every composed line before
	// it is drawn (see result.Input.Line). Its parts are already sanitised; nil
	// leaves the composed line unchanged.
	redact func(string) string

	theme    theme
	keys     keyMap
	progress progressState
	pane     logPane
	width    int
	height   int
	// now, loc and every are the clock, the zone log times are shown in, and the
	// redraw beat. Tests fix the first two and turn the beat off (zero).
	now   func() time.Time
	loc   *time.Location
	every time.Duration
	tick  int

	// splash is true while the splash is up; splashM draws it.
	splash  bool
	splashM *SplashModel
	// pre is the screens before the run, up while prePhase is true. Nil: the run starts
	// as soon as the splash ends (a progress-only run, or no wizard given).
	pre      *prerun
	prePhase bool
	// choose hands the plan the screens settled on to the run, before it starts.
	choose func(installflow.Engine, installflow.PreflightRunner)
	// leaving: the operator ended the screens before the run; the program ends once
	// the run, which is never started, has settled.
	leaving bool

	prompt   *promptMsg
	stopping bool
	summary  *installflow.Summary

	// op is the operation the run belongs to: install unless the presenter runs an
	// instance journey.
	op installflow.Operation
	// inst is the instance journey's screens (Presenter.Bootstrap), in place of pre. Its
	// screens before the run are up while prePhase is true.
	inst *instancePhase
	// continues says the cluster journey is followed by another: its verdict's Enter
	// leads on rather than out.
	continues bool
	// help is the help over the progress screen. It never pauses the run.
	help helpState
	// next, when set, is the command an install's success names for creating an instance
	// on the cluster, in the terminal UI (dcctl bootstrap ... --ui=tui).
	next func() string
}

// modelOptions are the parts of the screen that exist only for some runs.
type modelOptions struct {
	// version is shown on the splash; it may be empty. It is already sanitised and
	// redacted (splashVersion).
	version string
	// pre is the welcome and preflight screens; nil skips them.
	pre    *prerun
	choose func(installflow.Engine, installflow.PreflightRunner)
	clock  Clock
	// op, inst and continues: see model.
	op        installflow.Operation
	inst      *instancePhase
	continues bool
	// next, when set, is the command that creates an instance on the cluster an
	// install's success leaves; see model.
	next func() string
}

func newModel(caps terminal.Capabilities, intr *interrupter, start func(), logs logSource, redact func(string) string,
	o modelOptions) model {
	theme := newTheme(caps)
	updown := "↑/↓"
	if theme.g != unicodeGlyphs {
		updown = "up/down"
	}
	m := model{caps: caps, intr: intr, start: start, logs: logs, redact: redact, splash: caps.Splash != terminal.NoArt,
		theme: theme, keys: newKeyMap(updown), progress: newProgressState(),
		// The pane opens on its own only where there is room for it beside the rows.
		pane:  newLogPane(caps.Layout == terminal.Full),
		width: caps.Width, height: caps.Height,
		now: time.Now, loc: time.Local, every: tickEvery,
		pre: o.pre, prePhase: o.pre != nil, choose: o.choose,
		op: o.op, inst: o.inst, continues: o.continues, next: o.next}
	if o.inst != nil {
		m.progress.table = displayGroupsFor(installflow.OperationBootstrap)
		m.prePhase = !caps.ProgressOnly
	}
	if m.splash {
		clock := o.clock
		if clock == nil {
			clock = SystemClock{}
		}
		// The animated splash when the session is eligible for it (caps.Animate: never
		// over SSH, with NO_COLOR, --no-splash, --yes, --dev or --dry-run); otherwise the
		// static mark, or a one-line header where the terminal cannot draw the mark.
		m.splashM = NewSplash(brand.Options{Caps: caps, Subtitle: splashSubtitle, Version: o.version}, clock)
	}
	return m
}

func (m model) Init() tea.Cmd {
	if m.splash {
		return m.splashM.Init()
	}
	if m.prePhase {
		if m.inst != nil {
			return m.inst.start()
		}
		return nil
	}
	m.start()
	return m.beat()
}

// gated reports whether this run starts only on an approval given on screen. A
// progress-only instance run has no review: the command line approved it.
func (m model) gated() bool { return m.pre != nil || (m.inst != nil && !m.caps.ProgressOnly) }

// abandon ends the screens before the run without confirming it. The run they
// would have started is never started (see Presenter.Run); the program ends once
// that has settled.
func (m *model) abandon() {
	m.splash, m.prePhase, m.leaving, m.stopping = false, false, true, true
	m.start()
}

// begin starts the run with the plan the screens before it settled on. It is the
// confirmation: only Continue calls it.
func (m *model) begin() tea.Cmd {
	switch {
	case m.inst != nil && m.choose != nil:
		// The instance review's approval: the run built for the choices it showed.
		m.choose(m.inst.plan.Engine, m.inst.plan.Preflight)
	case m.pre != nil && m.choose != nil:
		m.choose(m.pre.engine, m.pre.runner)
	}
	m.prePhase = false
	m.start()
	return m.beat()
}

// beat schedules the next redraw, until the run has its verdict.
func (m model) beat() tea.Cmd {
	if m.every <= 0 || m.summary != nil {
		return nil
	}
	return tea.Tick(m.every, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.splashM != nil {
			m.splashM.Update(msg)
		}
		if m.pre != nil {
			m.pre.resize(msg.Width, msg.Height)
		}
		if m.inst != nil {
			m.inst.resize(msg.Width, msg.Height)
		}
	case tickMsg:
		m.tick++
		return m, m.beat()
	case splashTick:
		if m.splash {
			_, cmd := m.splashM.Update(msg)
			return m, cmd
		}
	case SplashDone:
		if m.splash {
			m.splash = false
			if !m.prePhase {
				m.start()
				return m, m.beat()
			}
		}
	case checkMsg:
		if m.pre != nil {
			// Drained after the run starts too, so the checks' goroutine never blocks.
			cmd, _ := m.pre.update(msg)
			return m, cmd
		}
	case settleMsg:
		// The settling of the screen whose Enter leads on: the instance journey's, or
		// the install review's.
		switch {
		case m.inst != nil && m.prePhase:
			cmd, _ := m.inst.update(msg)
			return m, cmd
		case m.pre != nil && m.prePhase:
			cmd, _ := m.pre.update(msg)
			return m, cmd
		}
	case InstanceChosen, EscrowChosen, ScreenBack, BootstrapPlanned:
		if m.inst != nil && m.prePhase {
			cmd, _ := m.inst.update(msg)
			return m, cmd
		}
	case SecretQuestion, SecretWithdrawn:
		// The run's escrow passphrase form, drawn over the progress screen while the
		// run waits on it.
		if m.inst != nil {
			_, cmd := m.inst.flow.Update(msg)
			return m, cmd
		}
		if q, ok := msg.(SecretQuestion); ok {
			// No screen here takes a secret: the question is refused, never left waiting.
			q.reply.answer(installflow.Response{})
		}
	case BootstrapDone:
		// The screens after a successful instance run are done; a failed run's verdict
		// stays on the progress screen until the operator leaves it.
		if m.inst != nil && m.summary != nil && m.summary.Succeeded() {
			return m, tea.Quit
		}
	case tea.PasteMsg:
		if m.inst != nil && (m.prePhase || m.inst.flow.pass != nil || m.inst.stage == instAfter) {
			if m.prePhase {
				cmd, _ := m.inst.update(msg)
				return m, cmd
			}
			_, cmd := m.inst.flow.Update(msg)
			return m, cmd
		}
	case stoppingMsg:
		m.stopping = true
		if m.gated() && (m.splash || m.prePhase) {
			// An OS interrupt before the run: the one cancel, like Exit or Ctrl+C.
			m.abandon()
		}
	case promptMsg:
		m.prompt = &msg
	case eventMsg:
		return m.event(msg.env)
	}
	return m, nil
}

func (m model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" && m.inst != nil && m.inst.stage == instAfter && m.summary != nil {
		// The instance run is over and its screens are up: there is nothing left to stop,
		// and its one-time disclosures are discarded on the way out (BootstrapFlow.Close).
		return m, tea.Quit
	}
	if k.String() == "ctrl+c" && m.continues && m.summary != nil && !m.caps.ProgressOnly {
		// The run is over and another journey would follow it: Ctrl+C is the operator
		// declining it. It is counted as the interrupt it is (the caller learns of it and
		// opens nothing more), and the program ends; there is nothing left to stop. It is
		// counted only if nothing was before it: an interrupt during the run, or a signal,
		// has already said so, and a second count would force the process out.
		m.intr.interruptOnce()
		return m, tea.Quit
	}
	if k.String() == "ctrl+c" && m.summary != nil && !m.caps.ProgressOnly {
		// The run is over and its verdict is on screen: Ctrl+C is the exit key, as Enter
		// is. 🔴 IT IS NOT COUNTED AN INTERRUPT. Counted, a second press (or one after an
		// interrupt during the run, or a signal) would force the process out with 1, or
		// with setup's forced status, before the exit summary is written: a succeeded run
		// would exit as if it had been abandoned, and "leaving never changes dcctl's exit
		// status" would be false.
		return m, tea.Quit
	}
	if k.String() == "ctrl+c" {
		// 🔴 NEVER tea.Quit HERE. The first interrupt cancels the run and the screen stays
		// until the engine has settled; the second ends the process through the
		// interrupter, which restores the terminal first.
		m.intr.interrupt()
		m.stopping = true
		switch {
		case m.gated() && (m.splash || m.prePhase):
			m.abandon() // before the run: nothing is started
		case m.splash:
			m.splash = false
			m.start() // the engine starts on a cancelled context and settles at once
			return m, m.beat()
		}
		return m, nil
	}
	if m.splash {
		// Any key skips the splash and is CONSUMED: it must not answer the first
		// question, nor act on the welcome screen. The splash says so with SplashDone.
		_, cmd := m.splashM.Update(k)
		return m, cmd
	}
	if m.prePhase && m.inst != nil {
		cmd, out := m.inst.update(k)
		switch out {
		case proceed:
			return m, tea.Batch(cmd, m.begin())
		case leave:
			m.intr.interrupt()
			m.abandon()
		}
		return m, cmd
	}
	if m.inst != nil && (m.inst.flow.pass != nil || m.inst.stage == instAfter) {
		// The escrow passphrase form during the run, and the screens after it.
		_, cmd := m.inst.flow.Update(k)
		return m, cmd
	}
	if m.prePhase {
		cmd, out := m.pre.update(k)
		switch out {
		case proceed:
			return m, tea.Batch(cmd, m.begin())
		case leave:
			// Exit before anything has run: the same cancel an interrupt is, and the
			// program ends as soon as the run has settled.
			m.intr.interrupt()
			m.abandon()
		}
		return m, cmd
	}
	if m.help.open || key.Matches(k, m.keys.help) {
		// 🔴 HELP TAKES THE KEY WHOLE: it answers no question (Esc closes help, it does
		// not answer no), moves no pane and leaves no screen. The run goes on under it.
		return m.helpKey(k), nil
	}
	if m.prompt != nil {
		switch k.String() {
		case "y", "Y", "n", "N", "enter", "esc":
			confirmed := k.String() == "y" || k.String() == "Y"
			m.prompt.reply <- installflow.Response{Confirmed: confirmed}
			m.prompt = nil
			return m, nil
		}
	}
	switch {
	case key.Matches(k, m.keys.logs):
		m.pane.toggle()
		return m, nil
	case key.Matches(k, m.keys.pageUp):
		m.pane.rows = m.logRows()
		m.pane.pageUp(m.logs)
		return m, nil
	case key.Matches(k, m.keys.pageDown):
		m.pane.rows = m.logRows()
		m.pane.pageDown(m.logs)
		return m, nil
	case key.Matches(k, m.keys.follow):
		m.pane.followTail()
		return m, nil
	}
	if m.prompt != nil {
		return m, nil // the question keeps the screen: nothing else answers or exits it
	}
	if m.summary != nil && !m.caps.ProgressOnly {
		switch k.String() {
		case "enter", "q", "esc":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) event(e installflow.Envelope) (tea.Model, tea.Cmd) {
	// The progress state is a map the model owns; folding an event into it is the
	// only change, and a copy of the model shares it, as Bubble Tea's loop expects.
	m.progress.apply(e)
	switch p := e.Payload.(type) {
	case installflow.PromptResolved:
		if m.prompt != nil && m.prompt.q.ID == p.ID {
			m.prompt = nil
		}
	case installflow.RunCancelling:
		m.stopping = true
	case installflow.Summary:
		s := p
		m.summary = &s
		m.prompt = nil
		if m.caps.ProgressOnly || m.leaving {
			// --yes: progress only, and the run's end is the program's end. Ended before
			// the run: nothing ran, so there is no result to read on screen.
			if m.inst != nil {
				m.inst.flow.Close()
			}
			return m, tea.Quit
		}
		if m.inst != nil {
			// The instance screens take the verdict: a success goes on to the receipt,
			// the first login and the instance success; a failure stays here.
			var facts InstanceFacts
			if s.Succeeded() && m.inst.scr.Facts != nil {
				facts = m.inst.scr.Facts(s)
			}
			if s.Succeeded() {
				m.inst.stage = instAfter
			}
			_, cmd := m.inst.flow.Update(BootstrapRunFinished{Summary: s, Facts: facts})
			return m, cmd
		}
	}
	return m, nil
}

// View draws the screen on the alternate screen, which the program leaves on exit.
func (m model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m model) render() string {
	if m.splash {
		return m.splashM.Splash().View()
	}
	if m.prePhase && m.inst != nil {
		return m.inst.render()
	}
	if m.prePhase {
		return m.pre.render()
	}
	if m.inst != nil && (m.inst.flow.pass != nil || m.inst.stage == instAfter) {
		return m.inst.flow.Render()
	}
	return m.renderProgress()
}

// questionText is the wording of each question the screen can ask, the same words the
// plain prompt uses. A code with no wording still shows its code, never an empty line.
func questionText(q installflow.Question) string { return questionWords(q, false) }

// questionWords is questionText, with the question's value replaced by the withheld
// marker when withhold is set: the fallback for a line that may not be drawn as composed.
func questionWords(q installflow.Question, withhold bool) string {
	if q.Question == installflow.QuestionCreateKindCluster {
		for _, p := range q.Params {
			if p.Name == "cluster" {
				if withhold {
					return "No kind cluster " + withheld + " found. Create it now?"
				}
				return fmt.Sprintf("No kind cluster %q found. Create it now?", p.Value.String())
			}
		}
	}
	return string(q.Question)
}

func outcomeWord(o installflow.RunOutcome) string {
	switch o {
	case installflow.RunSucceeded:
		return "succeeded"
	case installflow.RunPlanned:
		return "planned (dry run)"
	case installflow.RunCancelled:
		return "cancelled"
	case installflow.RunFailed:
		return "failed"
	}
	return "outcome unknown"
}
