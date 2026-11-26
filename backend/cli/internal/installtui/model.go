// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// splashHold is how long the static splash stays before the run starts on its own.
const splashHold = 500 * time.Millisecond

// logTail is how many of the hub's log lines the progress view shows.
const logTail = 6

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
)

// stepRow is one stage as the view shows it.
type stepRow struct {
	id     installflow.StepID
	status string
}

// model is the minimal install screen: a splash, the stage list with each stage's state
// as a word, the one open question, the stopping notice, the tail of the log, and the
// verdict. The real screens replace it; the bridge it is driven through does not change.
type model struct {
	caps terminal.Capabilities
	intr *interrupter
	// start lets the engine begin. It is idempotent.
	start func()
	logs  func() installflow.LogSnapshot

	splash   bool
	steps    []stepRow
	prompt   *promptMsg
	stopping bool
	summary  *installflow.Summary
}

func newModel(caps terminal.Capabilities, intr *interrupter, start func(), logs func() installflow.LogSnapshot) model {
	return model{caps: caps, intr: intr, start: start, logs: logs, splash: caps.Splash != terminal.NoArt}
}

func (m model) Init() tea.Cmd {
	if m.splash {
		return tea.Tick(splashHold, func(time.Time) tea.Msg { return splashDoneMsg{} })
	}
	m.start()
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.key(msg)
	case splashDoneMsg:
		if m.splash {
			m.splash = false
			m.start()
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
		return m, nil
	}
	if m.prompt != nil {
		var confirmed bool
		switch k.String() {
		case "y", "Y":
			confirmed = true
		case "n", "N", "enter", "esc":
		default:
			return m, nil
		}
		m.prompt.reply <- installflow.Response{Confirmed: confirmed}
		m.prompt = nil
		return m, nil
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
	switch p := e.Payload.(type) {
	case installflow.RunStarted:
		m.steps = m.steps[:0:0]
		for _, s := range p.Manifest {
			m.steps = append(m.steps, stepRow{id: s.ID, status: "waiting"})
		}
	case installflow.StepStarted:
		m.setStatus(e.StepID, "running")
	case installflow.StepFinished:
		m.setStatus(e.StepID, stepOutcomeWord(p.Outcome))
	case installflow.StepFailed:
		if p.Cancelled {
			m.setStatus(e.StepID, "cancelled")
		} else {
			m.setStatus(e.StepID, "failed")
		}
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

func (m *model) setStatus(id installflow.StepID, status string) {
	for i := range m.steps {
		if m.steps[i].id == id {
			m.steps[i].status = status
			return
		}
	}
	m.steps = append(m.steps, stepRow{id: id, status: status})
}

func stepOutcomeWord(o installflow.StepOutcome) string {
	switch o {
	case installflow.StepSucceeded:
		return "done"
	case installflow.StepUnchanged:
		return "unchanged"
	case installflow.StepPlanned:
		return "planned"
	}
	return "skipped"
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
	var b strings.Builder
	b.WriteString("DeviceChain · dcctl install\n\n")
	for _, s := range m.steps {
		if s.status == "skipped" {
			continue // not applicable to this run; JSON consumers still see it
		}
		fmt.Fprintf(&b, "  %-10s %s\n", s.status, s.id)
	}
	if m.prompt != nil {
		fmt.Fprintf(&b, "\n%s [y/N]\n", questionText(m.prompt.q))
	}
	if m.stopping && m.summary == nil {
		b.WriteString("\nInterrupt received. Waiting for the operation to stop safely. Nothing is being rolled back.\n" +
			"Press Ctrl+C again to exit immediately without waiting.\n")
	}
	if m.logs != nil {
		lines := m.logs().Lines
		if len(lines) > logTail {
			lines = lines[len(lines)-logTail:]
		}
		if len(lines) > 0 {
			b.WriteString("\n")
		}
		for _, l := range lines {
			fmt.Fprintf(&b, "  │ %s\n", l.Text.String())
		}
	}
	if m.summary != nil {
		fmt.Fprintf(&b, "\nInstall %s.\n", outcomeWord(m.summary.Outcome))
		if !m.caps.ProgressOnly {
			b.WriteString("Press Enter to exit.\n")
		}
	} else {
		b.WriteString("\nCtrl+C: stop\n")
	}
	return b.String()
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
