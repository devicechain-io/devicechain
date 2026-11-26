// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// BootstrapSetup is what the bootstrap screens start from.
type BootstrapSetup struct {
	Caps    terminal.Capabilities
	Version string
	// Target labels where the instance is built (a context or cluster name).
	Target string
	// TargetArgs are the target's flags as the command line had them, for the
	// equivalent command.
	TargetArgs []string
	Instance   InstanceSeed
	Escrow     EscrowSeed
	// Reveals is the run's RevealSink (Runtime.Reveals). The screens take its
	// offers after the run; Close discards whatever was never shown.
	Reveals *RevealInbox
	// Notes, when set, receives one line for each thing the screens did on the
	// run's behalf (the log pane's record of them). It never receives a secret.
	Notes io.Writer
}

// BootstrapPlanned is sent when the pre-run screens are settled: the parent
// shows the review and starts the run from these choices.
type BootstrapPlanned struct {
	Instance InstanceChoices
	Escrow   EscrowChoice
}

// BootstrapRunFinished tells the screens the run has returned its summary, and
// what the command layer read back.
type BootstrapRunFinished struct {
	Summary installflow.Summary
	Facts   InstanceFacts
}

// BootstrapDone is sent when the post-run screens are finished. The exit status
// is the summary's, whatever was acknowledged on the way.
type BootstrapDone struct{ Summary installflow.Summary }

type bsPhase int

const (
	phaseInstance bsPhase = iota
	phaseEscrow
	phaseRun
	phaseReceipt
	phaseFirstLogin
	phaseSuccess
	phaseDone
)

// BootstrapFlow is the instance journey's own screens: instance choices and the
// escrow plan before the run; the escrow passphrase form while the run asks for
// it; then the escrow receipt, the first login and the instance success. The
// review and progress screens between them belong to the parent program.
type BootstrapFlow struct {
	setup  BootstrapSetup
	theme  bsTheme
	phase  bsPhase
	help   bool
	inst   *instanceModel
	esc    *escrowModel
	pass   *passphraseModel
	rcpt   *receiptModel
	login  *firstLoginModel
	succ   *successModel
	choice BootstrapPlanned
	sum    installflow.Summary
}

// NewBootstrapFlow returns the flow at its first screen: instance choices, or,
// for a progress-only run (--yes), straight at the run.
func NewBootstrapFlow(s BootstrapSetup) *BootstrapFlow {
	f := &BootstrapFlow{setup: s, theme: newBSTheme(s.Caps)}
	f.inst = newInstanceModel(s.Instance)
	f.esc = newEscrowModel(s.Escrow)
	if s.Caps.ProgressOnly {
		f.choice = BootstrapPlanned{Instance: f.inst.choices(), Escrow: f.esc.choice()}
		f.phase = phaseRun
	}
	return f
}

// Init implements tea.Model.
func (f *BootstrapFlow) Init() tea.Cmd { return nil }

// Close clears everything secret the screens may still hold: a passphrase being
// typed, a password on screen, and every disclosure never shown. The program
// calls it on every exit path.
func (f *BootstrapFlow) Close() {
	if f.pass != nil {
		f.pass.clear()
		f.pass = nil
	}
	if f.login != nil {
		f.login.forget()
	}
	if f.setup.Reveals != nil {
		f.setup.Reveals.DiscardAll()
	}
}

// typing reports whether a text field has the focus, where "?" is a character.
func (f *BootstrapFlow) typing() bool {
	switch {
	case f.pass != nil:
		return true
	case f.phase == phaseInstance:
		return f.inst.input(f.inst.focus) != nil
	case f.phase == phaseEscrow:
		return f.esc.focus == escrowFieldPath
	}
	return false
}

// Update implements tea.Model.
func (f *BootstrapFlow) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.theme.width, f.theme.height = msg.Width, msg.Height
		return f, nil
	case SecretQuestion:
		if f.pass != nil { // the broker asks one at a time; a stale form is cleared
			f.pass.clear()
		}
		f.pass, f.help = newPassphraseModel(msg, f.setup.Notes), false
		return f, nil
	case SecretWithdrawn:
		if f.pass != nil && f.pass.q.Question.ID == msg.ID {
			f.pass.clear()
			f.pass = nil
		}
		return f, nil
	case InstanceChosen:
		f.choice.Instance = msg.Choices
		f.esc.setInstance(msg.Choices.Name)
		f.phase = phaseEscrow
		return f, nil
	case ScreenBack:
		if f.phase == phaseEscrow {
			f.phase = phaseInstance
		}
		return f, nil
	case EscrowChosen:
		f.choice.Escrow = msg.Choice
		f.phase = phaseRun
		planned := f.choice
		return f, func() tea.Msg { return planned }
	case BootstrapRunFinished:
		return f, f.finished(msg)
	case tea.KeyPressMsg:
		return f, f.key(msg)
	case tea.PasteMsg:
		return f, f.route(msg)
	}
	return f, nil
}

func (f *BootstrapFlow) key(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return nil // the program's cancel path, handled before a screen sees it
	}
	if f.help {
		if s == "esc" || s == "f1" || s == "?" {
			f.help = false
		}
		return nil
	}
	if s == "f1" || (s == "?" && !f.typing()) {
		f.help = true
		return nil
	}
	return f.route(k)
}

func (f *BootstrapFlow) route(msg tea.Msg) tea.Cmd {
	if f.pass != nil {
		if f.pass.update(msg) {
			f.pass = nil
		}
		return nil
	}
	switch f.phase {
	case phaseInstance:
		return f.inst.update(msg)
	case phaseEscrow:
		return f.esc.update(msg)
	case phaseReceipt:
		if f.rcpt.update(msg) {
			f.succ.acked = f.rcpt.acked
			f.advance()
		}
	case phaseFirstLogin:
		if f.login.update(msg) {
			f.succ.seenOnce = f.login.shown == nil && f.login.spent && f.login.stored
			f.login = nil
			f.advance()
		}
	case phaseSuccess:
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "enter" || k.String() == "q") {
			return f.done()
		}
	}
	return nil
}

func (f *BootstrapFlow) finished(msg BootstrapRunFinished) tea.Cmd {
	f.sum = msg.Summary
	if f.pass != nil {
		f.pass.clear()
		f.pass = nil
	}
	var offers []installflow.RevealOffer
	if f.setup.Reveals != nil {
		offers = f.setup.Reveals.Take()
	}
	if !msg.Summary.Succeeded() || f.setup.Caps.ProgressOnly {
		// A failed run's screens are the parent's; a progress-only run reveals
		// nothing and waits on nobody.
		for _, o := range offers {
			o.Reveal.Discard()
		}
		return f.done()
	}
	f.succ = &successModel{instance: f.choice.Instance, escrow: f.choice.Escrow, facts: msg.Facts,
		target: f.setup.TargetArgs}
	for _, c := range msg.Summary.Credentials {
		ref := c
		f.succ.seedRef = &ref
	}
	for _, o := range offers {
		if f.login == nil && o.Subject == installflow.RevealSuperuserSeed && !o.Reveal.Consumed() {
			where := o.Where
			f.succ.seedRef = &where
			f.login = &firstLoginModel{offer: o, account: orDefault(msg.Facts.Account, "the superuser")}
			continue
		}
		o.Reveal.Discard()
	}
	if f.choice.Escrow.Mode == EscrowWrite && msg.Facts.EscrowPath != "" {
		f.rcpt = &receiptModel{path: msg.Facts.EscrowPath, written: msg.Facts.EscrowWritten,
			instance: f.choice.Instance.Name, context: msg.Facts.KubeContext}
		f.phase = phaseReceipt
		return nil
	}
	f.phase = phaseReceipt
	f.advance()
	return nil
}

// advance moves to the next post-run screen that applies.
func (f *BootstrapFlow) advance() {
	switch f.phase {
	case phaseReceipt:
		f.rcpt = nil
		f.phase = phaseFirstLogin
		if f.login != nil {
			return
		}
		fallthrough
	case phaseFirstLogin:
		f.phase = phaseSuccess
	}
}

func (f *BootstrapFlow) done() tea.Cmd {
	f.phase = phaseDone
	if f.setup.Reveals != nil {
		f.setup.Reveals.DiscardAll()
	}
	sum := f.sum
	return func() tea.Msg { return BootstrapDone{Summary: sum} }
}

// ExitSummary writes the non-secret record left on stdout after the program.
func (f *BootstrapFlow) ExitSummary(w io.Writer) {
	if f.succ != nil && f.sum.Succeeded() {
		f.succ.exitSummary(w)
	}
}

func (f *BootstrapFlow) header() string {
	parts := []string{"DeviceChain", "dcctl bootstrap"}
	if n := f.choice.Instance.Name; n != "" {
		parts = append(parts, "instance "+n)
	} else if n := f.setup.Instance.Name; n != "" {
		parts = append(parts, "instance "+n)
	}
	if f.setup.Target != "" {
		parts = append(parts, f.setup.Target)
	}
	if f.setup.Version != "" {
		parts = append(parts, f.setup.Version)
	}
	return strings.Join(parts, f.theme.dot())
}

func (f *BootstrapFlow) current() bsScreen {
	t := f.theme
	if f.pass != nil {
		return f.pass.screen(t)
	}
	switch f.phase {
	case phaseInstance:
		return f.inst.screen(t)
	case phaseEscrow:
		return f.esc.screen(t)
	case phaseReceipt:
		return f.rcpt.screen(t)
	case phaseFirstLogin:
		return f.login.screen(t)
	case phaseSuccess:
		return f.succ.screen(t)
	}
	return bsScreen{title: "Bootstrap", body: t.para(rolePlain, "", "The run is in progress.", t.contentWidth())}
}

func (f *BootstrapFlow) helpScreen(under bsScreen) bsScreen {
	t := f.theme
	var body []string
	body = append(body, t.para(rolePlain, "", "Keys on the "+strings.ToLower(under.title)+" screen:", t.contentWidth())...)
	for _, b := range under.keys {
		h := b.Help()
		body = append(body, "  "+t.paint(roleLabel, padRight(h.Key, 12))+h.Desc)
	}
	body = append(body, "", t.paint(roleDim, "Ctrl+C stops the run safely; Esc never does."))
	return bsScreen{title: "Help", body: body, keys: []key.Binding{
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc/F1", "close help"))}}
}

// Render is the current screen as text, for the parent to compose.
func (f *BootstrapFlow) Render() string {
	s := f.current()
	if f.help {
		s = f.helpScreen(s)
	}
	return f.theme.frame(f.header(), s)
}

// View implements tea.Model.
func (f *BootstrapFlow) View() tea.View { return tea.NewView(f.Render()) }
