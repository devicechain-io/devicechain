// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// splashHold is how long the static splash stays before the run starts on its own.
const splashHold = 500 * time.Millisecond

// tickEvery is the progress screen's redraw beat while a run is live: the clock line
// and the moving marker. It is 4 redraws a second, under the 12 the splash may use.
const tickEvery = 250 * time.Millisecond

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
	// splashDoneMsg ends the splash when nobody pressed a key.
	splashDoneMsg struct{}
	// tickMsg redraws the clock line and the moving marker.
	tickMsg struct{}
)

// model is the install screen: a splash, then the progress screen (the overall bar, a
// row per display stage with its own bar, the clock and budget line, the log pane), the
// one open question, the stopping notice, and the verdict.
type model struct {
	caps terminal.Capabilities
	intr *interrupter
	// start lets the engine begin. It is idempotent.
	start func()
	logs  logSource

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

	splash   bool
	prompt   *promptMsg
	stopping bool
	summary  *installflow.Summary
}

func newModel(caps terminal.Capabilities, intr *interrupter, start func(), logs logSource) model {
	return model{caps: caps, intr: intr, start: start, logs: logs, splash: caps.Splash != terminal.NoArt,
		theme: newTheme(caps), keys: newKeyMap(), progress: newProgressState(),
		// The pane opens on its own only where there is room for it beside the rows.
		pane:  newLogPane(caps.Layout == terminal.Full),
		width: caps.Width, height: caps.Height,
		now: time.Now, loc: time.Local, every: tickEvery}
}

func (m model) Init() tea.Cmd {
	if m.splash {
		return tea.Tick(splashHold, func(time.Time) tea.Msg { return splashDoneMsg{} })
	}
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
	case tickMsg:
		m.tick++
		return m, m.beat()
	case splashDoneMsg:
		if m.splash {
			m.splash = false
			m.start()
			return m, m.beat()
		}
	case stoppingMsg:
		m.stopping = true
	case promptMsg:
		m.prompt = &msg
	case eventMsg:
		return m.event(msg.env)
	}
	return m, nil
}

func (m model) key(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		// 🔴 NEVER tea.Quit HERE. The first interrupt cancels the run and the screen stays
		// until the engine has settled; the second ends the process through the
		// interrupter, which restores the terminal first.
		m.intr.interrupt()
		m.stopping = true
		if m.splash {
			m.splash = false
			m.start() // the engine starts on a cancelled context and settles at once
		}
		return m, nil
	}
	if m.splash {
		// Any key skips the splash and is CONSUMED: it must not answer the first question.
		m.splash = false
		m.start()
		return m, m.beat()
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
		if m.caps.ProgressOnly {
			// --yes: progress only, and the run's end is the program's end.
			return m, tea.Quit
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
		return "\n  DeviceChain\n  Prepare a Kubernetes cluster\n\n  press any key to continue\n"
	}
	return m.renderProgress()
}

// questionText is the wording of each question the screen can ask, the same words the
// plain prompt uses. A code with no wording still shows its code, never an empty line.
func questionText(q installflow.Question) string {
	if q.Question == installflow.QuestionCreateKindCluster {
		for _, p := range q.Params {
			if p.Name == "cluster" {
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
