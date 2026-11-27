// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// EscrowMode is what happens to the instance's root key.
type EscrowMode string

const (
	// EscrowWrite writes an encrypted escrow artifact: the default, always.
	EscrowWrite EscrowMode = "write"
	// EscrowNone is --no-escrow: chosen explicitly, behind the permanent-loss warning.
	EscrowNone EscrowMode = "none"
	// EscrowRestore is --restore-root-key: the key comes from an artifact the
	// command line named. The screen shows it and changes nothing about it.
	EscrowRestore EscrowMode = "restore"
)

// PassphraseSource is where the escrow passphrase will come from, in the
// engine's order: a protected file, the environment, then a person.
type PassphraseSource string

const (
	PassphraseFromPerson PassphraseSource = ""
	PassphraseFromFile   PassphraseSource = "file"
	PassphraseFromEnv    PassphraseSource = "environment"
)

// EscrowSeed is what the escrow screen starts from.
type EscrowSeed struct {
	// Mode is the mode the command line asked for; the zero value is EscrowWrite.
	Mode EscrowMode
	// Path is --escrow-file when given (PathFromFlag), and otherwise empty: the
	// default for the chosen instance name comes from DefaultPath.
	Path         string
	PathFromFlag bool
	// RestoreFrom is --restore-root-key's artifact, in EscrowRestore.
	RestoreFrom string
	// Passphrase says whether a file or the environment already supplies it, so
	// the screen asks for nothing.
	Passphrase PassphraseSource
	// DefaultPath is the engine's default artifact path for an instance name.
	DefaultPath func(instance string) string
	// ValidatePath is the engine's path rule for the instance named on the
	// instance screen (outside that instance's state directory, which destroy
	// deletes). Nil accepts anything.
	ValidatePath func(instance, path string) error
}

// EscrowChoice is the escrow the person settled on.
type EscrowChoice struct {
	Mode EscrowMode
	// Path is the absolute artifact path in EscrowWrite, and the artifact in
	// EscrowRestore.
	Path string
	// PathGiven says the path is to be passed as --escrow-file.
	PathGiven  bool
	Passphrase PassphraseSource
}

// EscrowChosen is sent when the person continues from the escrow screen.
type EscrowChosen struct{ Choice EscrowChoice }

// WordsEscrowExplained is the escrow screen's explanation.
const WordsEscrowExplained = "DeviceChain encrypts stored credentials with this instance's root key. " +
	"Database backups do not contain it. To recover after losing the cluster you need this escrow file " +
	"and its passphrase. Copy the file off this machine; keep the passphrase separately."

// WordsNoEscrowWarning is shown whenever --no-escrow is the selected choice.
const WordsNoEscrowWarning = "WARNING: PERMANENT LOSS. Without an escrow file the root key exists only inside " +
	"the cluster. If the cluster is lost, every stored secret becomes permanently unreadable, even from a " +
	"database backup. Choose this only for a throwaway instance."

type escrowField int

const (
	escrowFieldWrite escrowField = iota
	escrowFieldNone
	escrowFieldPath
	escrowFieldBack
	escrowFieldContinue
	escrowFieldCount
)

// escrowModel is screen S3c, the escrow plan: mode and location. The passphrase
// is not asked here; the engine asks for it through the run's prompt broker when
// the run reaches its escrow stage, before it changes anything.
type escrowModel struct {
	seed     EscrowSeed
	instance string
	mode     EscrowMode
	path     textinput.Model
	custom   bool // the path field was edited, or came from --escrow-file
	focus    escrowField
	err      string
}

func newEscrowModel(seed EscrowSeed) *escrowModel {
	m := &escrowModel{seed: seed, mode: seed.Mode}
	if m.mode == "" {
		m.mode = EscrowWrite
	}
	m.path = newTextField(seed.Path)
	m.custom = seed.PathFromFlag && seed.Path != ""
	if m.mode == EscrowRestore {
		m.focus = escrowFieldContinue
	}
	return m
}

// setInstance follows the instance name into the default path, unless the
// person chose a path of their own.
func (m *escrowModel) setInstance(name string) {
	m.instance = name
	if !m.custom && m.seed.DefaultPath != nil {
		m.path.SetValue(m.seed.DefaultPath(name))
	}
}

func (m *escrowModel) setFocus(f escrowField) {
	m.path.Blur()
	m.focus = f
	if f == escrowFieldPath {
		_ = m.path.Focus()
	}
}

func (m *escrowModel) stops() []escrowField {
	switch {
	case m.mode == EscrowRestore:
		return []escrowField{escrowFieldBack, escrowFieldContinue}
	case m.mode == EscrowNone:
		return []escrowField{escrowFieldWrite, escrowFieldNone, escrowFieldBack, escrowFieldContinue}
	}
	return []escrowField{escrowFieldWrite, escrowFieldNone, escrowFieldPath, escrowFieldBack, escrowFieldContinue}
}

func (m *escrowModel) move(d int) {
	stops := m.stops()
	i := 0
	for j, f := range stops {
		if f == m.focus {
			i = j
		}
	}
	m.setFocus(stops[(i+d+len(stops))%len(stops)])
}

func (m *escrowModel) keys() []key.Binding {
	out := []key.Binding{bsKeyNext, bsKeyPrev}
	if m.focus == escrowFieldWrite || m.focus == escrowFieldNone {
		out = append(out, bsKeyToggle)
	}
	return append(out, bsKeyEnter, bsKeyBack, bsKeyHelp)
}

func (m *escrowModel) update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if p, ok := msg.(tea.PasteMsg); ok && m.focus == escrowFieldPath {
			m.path, _ = m.path.Update(p)
			m.custom = true
		}
		return nil
	}
	switch k.String() {
	case "tab", "down":
		m.move(1)
		return nil
	case "shift+tab", "up":
		m.move(-1)
		return nil
	case "esc":
		return func() tea.Msg { return ScreenBack{} }
	case "space", " ", "enter":
		switch m.focus {
		case escrowFieldWrite:
			m.mode = EscrowWrite
			return nil
		case escrowFieldNone:
			// Selecting it is the explicit choice; the warning is on screen from
			// this moment, and Continue is a separate, later action.
			m.mode = EscrowNone
			return nil
		case escrowFieldBack:
			if k.String() == "enter" {
				return func() tea.Msg { return ScreenBack{} }
			}
		case escrowFieldContinue:
			if k.String() == "enter" {
				return m.submit()
			}
		}
	}
	if m.focus == escrowFieldPath {
		before := m.path.Value()
		m.path, _ = m.path.Update(k)
		if m.path.Value() != before {
			m.custom, m.err = true, ""
		}
	}
	return nil
}

func (m *escrowModel) choice() EscrowChoice {
	c := EscrowChoice{Mode: m.mode, Passphrase: m.seed.Passphrase}
	switch m.mode {
	case EscrowWrite:
		c.Path = strings.TrimSpace(m.path.Value())
		c.PathGiven = m.custom
	case EscrowRestore:
		c.Path = m.seed.RestoreFrom
	}
	return c
}

func (m *escrowModel) submit() tea.Cmd {
	c := m.choice()
	m.err = ""
	if c.Mode == EscrowWrite {
		switch {
		case c.Path == "":
			m.err = "an escrow file path is required"
		case m.seed.ValidatePath != nil:
			if err := m.seed.ValidatePath(m.instance, c.Path); err != nil {
				m.err = err.Error()
			}
		}
		if m.err != "" {
			m.setFocus(escrowFieldPath)
			return nil
		}
	}
	return func() tea.Msg { return EscrowChosen{Choice: c} }
}

func (m *escrowModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	var body []string
	body = append(body, t.para(rolePlain, "", WordsEscrowExplained, w)...)
	body = append(body, "")
	if m.mode == EscrowRestore {
		body = append(body, t.field("Root key", "restored from the escrow artifact "+m.seed.RestoreFrom+
			" (--restore-root-key)", 12, w)...)
		body = append(body, t.para(rolePlain, "  ", "Its passphrase is asked for when the run reaches the escrow "+
			"step, before anything is changed, unless a file or the environment supplies it.", w)...)
	} else {
		opt := func(f escrowField, on bool, label string) {
			body = append(body, t.marked(m.focus == f, t.radio(on)+" "+label, w)...)
		}
		opt(escrowFieldWrite, m.mode == EscrowWrite, "Write an encrypted escrow file (recommended)")
		opt(escrowFieldNone, m.mode == EscrowNone, "Do not escrow the root key (--no-escrow)")
		body = append(body, "")
		if m.mode == EscrowNone {
			body = append(body, t.para(roleWarn, "  ", WordsNoEscrowWarning, w)...)
		} else {
			v, role := m.path.Value(), rolePlain
			mark := "  "
			if m.focus == escrowFieldPath {
				r := []rune(v)
				pos := min(m.path.Position(), len(r))
				v, role, mark = string(r[:pos])+t.caret()+string(r[pos:]), roleFocus, t.focusMark()+" "
			}
			for i, part := range wrap(v, w-12) {
				lead := strings.Repeat(" ", 12)
				if i == 0 {
					lead = mark + t.paint(roleLabel, padRight("Location", 10))
				}
				body = append(body, lead+t.paint(role, part))
			}
			note := "default for this instance, on this machine, outside what 'dcctl destroy' removes"
			if m.custom {
				note = "passed as --escrow-file; an existing file is refused, never overwritten"
			}
			body = append(body, t.para(roleDim, strings.Repeat(" ", 12), note, w)...)
			if m.err != "" {
				body = append(body, t.para(roleBad, strings.Repeat(" ", 12), "ERROR: "+m.err, w)...)
			}
			body = append(body, "")
			body = append(body, t.para(rolePlain, "  ", passphraseNote(m.seed.Passphrase), w)...)
		}
	}
	body = append(body, "", "  "+button(t, "Back", m.focus == escrowFieldBack)+"   "+
		button(t, "Continue", m.focus == escrowFieldContinue))
	return bsScreen{title: "Root-key escrow", body: body, keys: m.keys(), before: true}
}

func passphraseNote(src PassphraseSource) string {
	switch src {
	case PassphraseFromFile:
		return "Passphrase supplied by protected file (--escrow-passphrase-file). Nothing will be asked."
	case PassphraseFromEnv:
		return "Passphrase supplied by the environment. Nothing will be asked."
	}
	return "You will be asked for its passphrase, twice, when the run reaches the escrow step, " +
		"before anything is changed. Use a long unique passphrase from your password manager."
}

// SecretQuestion is a secret prompt from the run's broker, delivered into the
// program by SecretResponder. Answer it exactly once.
type SecretQuestion struct {
	Question installflow.Question
	reply    *secretReply
}

// SecretWithdrawn is sent when the run stops waiting for the question's answer.
type SecretWithdrawn struct{ ID installflow.PromptID }

// secretReply is the one answer to a question. An answer that arrives after the
// run stopped waiting is cleared rather than left in a channel nobody reads.
type secretReply struct {
	mu     sync.Mutex
	ch     chan installflow.Response
	closed bool
}

func (r *secretReply) answer(resp installflow.Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		resp.Secret.Zero()
		return
	}
	r.closed = true
	r.ch <- resp // buffered for exactly this one answer
}

func (r *secretReply) withdraw() (installflow.Response, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	select {
	case resp := <-r.ch:
		return resp, true
	default:
		return installflow.Response{}, false
	}
}

// SecretResponder answers the run's secret prompts with the passphrase form. It
// is an installflow.Responder: Send delivers the question into the program, and
// Respond waits for the person's answer or for ctx. It answers secret questions
// only; any other kind is ErrPromptUnavailable here and belongs to the responder
// it is composed with.
type SecretResponder struct {
	Send func(tea.Msg)
}

// Respond implements installflow.Responder.
func (r SecretResponder) Respond(ctx context.Context, q installflow.Question) (installflow.Response, error) {
	if q.Kind != installflow.PromptSecret || r.Send == nil {
		return installflow.Response{}, installflow.ErrPromptUnavailable
	}
	reply := &secretReply{ch: make(chan installflow.Response, 1)}
	r.Send(SecretQuestion{Question: q, reply: reply})
	select {
	case resp := <-reply.ch:
		return resp, nil
	case <-ctx.Done():
		if resp, ok := reply.withdraw(); ok {
			resp.Secret.Zero()
		}
		r.Send(SecretWithdrawn{ID: q.ID})
		return installflow.Response{}, ctx.Err()
	}
}

// passphraseModel is the escrow entry form, shown while the run waits on a
// secret question. Nothing typed is ever drawn: no characters, no mask, no
// count. The value lives in a secretField until Enter hands it to the run as an
// installflow.Secret, and the field is wiped at that moment.
type passphraseModel struct {
	q     SecretQuestion
	field secretField
	empty bool
	notes io.Writer
}

func newPassphraseModel(q SecretQuestion, notes io.Writer) *passphraseModel {
	return &passphraseModel{q: q, notes: notes}
}

// secretField is the passphrase's one buffer, owned here so it can be wiped. A
// Bubbles textinput cannot hold it: its runes are unexported and Reset only drops
// the slice, leaving the passphrase in the abandoned array. Every array this
// field outgrows is zeroed as it is replaced, and wipe zeroes the current one.
// The strings a terminal delivers keys and pastes in are Go strings, which no
// one can clear; nothing here claims otherwise. Nor can this field reach the
// copy the run keeps once the answer is submitted: the broker registers the
// value with the hub's redactor, which must hold it to redact it for the rest
// of the run.
type secretField struct{ buf []byte }

// insert appends text, dropping control characters.
func (f *secretField) insert(text string) {
	for _, r := range text {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			continue
		}
		var enc [utf8.UTFMax]byte
		n := utf8.EncodeRune(enc[:], r)
		if len(f.buf)+n > cap(f.buf) {
			grown := make([]byte, len(f.buf), max(64, 2*(len(f.buf)+n)))
			copy(grown, f.buf)
			clear(f.buf[:cap(f.buf)])
			f.buf = grown
		}
		f.buf = append(f.buf, enc[:n]...)
		clear(enc[:])
	}
}

// backspace removes the last character and zeroes its bytes.
func (f *secretField) backspace() {
	if len(f.buf) == 0 {
		return
	}
	_, n := utf8.DecodeLastRune(f.buf)
	clear(f.buf[len(f.buf)-n:])
	f.buf = f.buf[:len(f.buf)-n]
}

// wipe zeroes the whole array and empties the field.
func (f *secretField) wipe() {
	clear(f.buf[:cap(f.buf)])
	f.buf = f.buf[:0]
}

var (
	bsKeySubmit = key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "submit"))
	bsKeyClear  = key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "clear"))
	bsKeyF1     = key.NewBinding(key.WithKeys("f1"), key.WithHelp("F1", "help"))
)

func (m *passphraseModel) update(msg tea.Msg) (answered bool) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.field.insert(msg.Content)
		m.empty = false
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			if len(m.field.buf) == 0 {
				m.empty = true
				return false
			}
			secret := installflow.NewSecret(m.field.buf)
			m.field.wipe()
			m.note("escrow passphrase entered; it is not shown and not kept by the screen")
			m.q.reply.answer(installflow.Response{Secret: secret})
			return true
		case "esc", "ctrl+u":
			// Esc never stops the run; here it empties the field.
			m.field.wipe()
			return false
		case "backspace":
			m.field.backspace()
			return false
		}
		m.field.insert(msg.Text)
		m.empty = false
	}
	return false
}

// clear wipes the field: on withdrawal, and when the program ends.
func (m *passphraseModel) clear() { m.field.wipe() }

func (m *passphraseModel) note(s string) {
	if m.notes != nil {
		_, _ = fmt.Fprintln(m.notes, s)
	}
}

func (m *passphraseModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	var title, ask string
	switch m.q.Question.Question {
	case installflow.QuestionEscrowPassphraseConfirm:
		title, ask = "Confirm the escrow passphrase", "Type the same passphrase again."
	case installflow.QuestionEscrowRestorePassphrase:
		title, ask = "Escrow passphrase", "Passphrase for the root-key escrow "+paramOf(m.q.Question, "artifact")+"."
	default:
		title, ask = "Escrow passphrase", "Choose the passphrase that protects the root-key escrow for instance "+
			paramOf(m.q.Question, "instance")+"."
	}
	var body []string
	body = append(body, t.para(rolePlain, "", ask, w)...)
	body = append(body, "")
	body = append(body, t.focusMark()+" "+t.paint(roleLabel, "Passphrase  ")+t.caret()+
		"  "+t.paint(roleDim, "(typing is not shown)"))
	if m.empty {
		body = append(body, t.para(roleBad, "  ", "ERROR: the passphrase cannot be empty.", w)...)
	}
	body = append(body, "")
	if m.q.Question.Question == installflow.QuestionEscrowPassphrase {
		body = append(body, t.para(roleDim, "", "Use a long unique passphrase from your password manager. "+
			"Pasting is accepted. It is never shown, logged or stored by this screen.", w)...)
	} else {
		body = append(body, t.para(roleDim, "", "It is never shown, logged or stored by this screen.", w)...)
	}
	return bsScreen{title: title, body: body, keys: []key.Binding{bsKeySubmit, bsKeyClear, bsKeyF1}}
}

func paramOf(q installflow.Question, name string) string {
	for _, p := range q.Params {
		if p.Name == name {
			return p.Value.String()
		}
	}
	return "this instance"
}

// receiptModel is screen S6b, the escrow receipt and its custody
// acknowledgement. The acknowledgement starts unchecked, is changed only by the
// person (Space), and never changes the run's outcome or exit status.
type receiptModel struct {
	path     string
	written  bool
	instance string
	context  string
	acked    bool
}

var bsKeyContinue = key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "continue"))

func (m *receiptModel) update(msg tea.Msg) (done bool) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "space", " ":
		m.acked = !m.acked
	case "enter":
		return true
	}
	return false
}

func (m *receiptModel) verifyCommand() string {
	cmd := "dcctl secrets escrow verify " + shellQuote(m.path) + " --instance " + shellQuote(m.instance)
	if m.context != "" {
		cmd += " --kube-context " + shellQuote(m.context)
	}
	return cmd
}

func (m *receiptModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	var body []string
	if m.written {
		body = append(body, t.para(roleGood, "", "Root-key escrow written and checked:", w)...)
	} else {
		body = append(body, t.para(rolePlain, "", "Root-key escrow already in place (this run did not write it):", w)...)
	}
	body = append(body, t.para(roleLabel, "  ", m.path, w)...)
	body = append(body, "")
	body = append(body, t.para(rolePlain, "", "Copy this file off this machine now, and keep its passphrase "+
		"somewhere else. Without both, a lost cluster cannot be recovered.", w)...)
	body = append(body, "")
	body = append(body, t.marked(true, t.check(m.acked)+" I have stored the escrow file off this machine and its passphrase separately", w)...)
	if !m.acked {
		body = append(body, t.para(roleWarn, "  ", "Off-site custody not confirmed.", w)...)
	}
	body = append(body, "")
	body = append(body, t.para(roleDim, "", "This is a reminder, not a backup test. To check the file "+
		"opens with its passphrase:", w)...)
	body = append(body, t.para(rolePlain, "  ", m.verifyCommand(), w)...)
	return bsScreen{title: "Escrow receipt", body: body, keys: []key.Binding{bsKeyToggle, bsKeyContinue, bsKeyHelp}}
}

// shellQuote quotes a value for a POSIX shell, leaving plain words bare.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
