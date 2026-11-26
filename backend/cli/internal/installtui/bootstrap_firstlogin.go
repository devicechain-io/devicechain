// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// RevealInbox is the terminal UI's installflow.RevealSink. It holds the run's
// one-time disclosures, without showing or copying them, until the first-login
// screen takes them after the run; Offer never blocks. Whatever is never taken
// is discarded by DiscardAll, which the program calls on every exit path.
type RevealInbox struct {
	mu     sync.Mutex
	offers []installflow.RevealOffer
	closed bool
}

// Offer implements installflow.RevealSink.
func (b *RevealInbox) Offer(o installflow.RevealOffer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		o.Reveal.Discard()
		return
	}
	b.offers = append(b.offers, o)
}

// Take hands over every offer held, and holds none afterwards.
func (b *RevealInbox) Take() []installflow.RevealOffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.offers
	b.offers = nil
	return out
}

// DiscardAll consumes every offer still held, unshown, and discards any that
// arrives later.
func (b *RevealInbox) DiscardAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, o := range b.offers {
		o.Reveal.Discard()
	}
	b.offers, b.closed = nil, true
}

// firstLoginModel is screen S7a: an explicit, one-time reveal of the generated
// superuser seed password.
//
// 🔴 SHOWN ONCE, ON AN EXPLICIT ACTION, AND GONE AFTER THE ACKNOWLEDGEMENT. The
// screen opens with the value hidden; "s" (or Enter on Show once) consumes the
// Reveal and copies the value into shown, the only place it is held. From then on
// the screen can be left only through the acknowledgement: Space ticks "I have
// stored it", and Enter then clears shown and leaves. Help replaces the whole
// screen (BootstrapFlow.Render), so it hides the value first.
// A second "Show once" has nothing to show: the Reveal is spent.
type firstLoginModel struct {
	offer   installflow.RevealOffer
	account string
	shown   []byte // the value, between the reveal and the acknowledgement
	spent   bool   // the reveal happened; it never happens again
	stored  bool   // the person ticked "I have stored it"
	nudge   bool   // Enter was pressed before the tick
	focus   int    // before the reveal: 0 Show once, 1 Skip
}

var (
	bsKeyShow  = key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "show once"))
	bsKeyStore = key.NewBinding(key.WithKeys("space", " "), key.WithHelp("Space", "I have stored it"))
	bsKeyDone  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "done"))
	bsKeyMove  = key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("Tab", "next"))
)

func (m *firstLoginModel) keys() []key.Binding {
	if m.spent {
		return []key.Binding{bsKeyStore, bsKeyDone, bsKeyHelp}
	}
	return []key.Binding{bsKeyShow, bsKeyMove, bsKeyEnter, bsKeyHelp}
}

// update reports whether the screen is finished.
func (m *firstLoginModel) update(msg tea.Msg) (done bool) {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	if !m.spent {
		switch k.String() {
		case "tab", "shift+tab", "up", "down", "left", "right":
			m.focus = 1 - m.focus
		case "s":
			m.reveal()
		case "enter":
			if m.focus == 0 {
				m.reveal()
				return false
			}
			m.offer.Reveal.Discard() // skipped: it stays only in the Secret
			m.spent = true
			return true
		}
		return false
	}
	switch k.String() {
	case "space", " ":
		m.stored = !m.stored
		m.nudge = false
	case "enter":
		if !m.stored && m.shown != nil {
			m.nudge = true
			return false
		}
		m.forget()
		return true
	}
	return false
}

func (m *firstLoginModel) reveal() {
	if m.spent {
		return
	}
	m.spent = true
	_ = m.offer.Reveal.Show(func(v []byte) { m.shown = append([]byte(nil), v...) })
}

// forget clears the value. It is called on the acknowledgement and when the
// program ends, whichever comes first.
func (m *firstLoginModel) forget() {
	clear(m.shown)
	m.shown = nil
	m.offer.Reveal.Discard()
}

func (m *firstLoginModel) screen(t bsTheme) bsScreen {
	w := t.contentWidth()
	where := m.offer.Where
	var body []string
	body = append(body, t.field("Account", m.account, 9, w)...)
	body = append(body, "")
	switch {
	case !m.spent:
		body = append(body, t.para(rolePlain, "", "A password was generated for this account. It can be shown once, "+
			"here, and dcctl will not show it again. Have your password manager ready.", w)...)
		body = append(body, "")
		body = append(body, "  "+button(t, "Show once", m.focus == 0)+"   "+
			button(t, "Skip (read it from the Secret later)", m.focus == 1))
	case m.shown != nil:
		body = append(body, "  "+t.paint(roleLabel, "Password ")+t.paint(roleSecret, string(m.shown)))
		body = append(body, "")
		body = append(body, t.para(roleWarn, "", "Store it now: this is the only time it is shown. Sign in to the "+
			"admin console to create your first tenant, and change it there.", w)...)
		body = append(body, "")
		body = append(body, t.focusMark()+" "+t.paint(roleFocus, t.check(m.stored)+" I have stored this password"))
		if m.nudge {
			body = append(body, t.para(roleBad, "  ", "Tick \"I have stored this password\" (Space) before leaving: "+
				"once this screen is left the password is not shown again.", w)...)
		}
	default:
		body = append(body, t.para(rolePlain, "", "The password could not be shown.", w)...)
	}
	body = append(body, "")
	body = append(body, t.para(roleDim, "", "The seed password is also kept in Secret "+where.Namespace+"/"+where.Name+
		", key "+where.Key+" (changing the password in the console does not update it).", w)...)
	return bsScreen{title: "First login", body: body, keys: m.keys()}
}
