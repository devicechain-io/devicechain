// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
)

// Help over the install's screens: ? or F1 opens it on every screen (F1 alone where ? is
// typed into a field), Esc, ? or F1 closes it, and the arrows and PgUp/PgDn scroll it.
// Its body is the result screen's help (result.HelpLines): a row for every key the
// footer under it draws, from the same bindings, then what the screen does.
//
// 🔴 HELP CHANGES NOTHING. While it is open it takes every key but Ctrl+C, which keeps
// its own meaning (the program's cancel path takes it before any screen does): nothing
// under it moves, changes or answers a question. On a screen whose Enter leads on (the
// install review, setup's hand-over and instance review) each of its keys is input to
// the settle gate, so the screen settles again once help closes (settleGate.interrupt).

// helpScrollKeys scroll help when it is taller than its room.
var helpScrollKeys = []string{"up", "down", "pgup", "pgdown"}

// helpState is help over a screen: whether it is open, and its first line shown.
type helpState struct {
	open   bool
	offset int
}

// scroll moves help by the key k, over n lines shown in room: a line for an arrow, a
// page for PgUp/PgDn, never past either end.
func (h *helpState) scroll(k string, n, room int) {
	page := max(room-1, 1)
	switch k {
	case "up":
		h.offset--
	case "down":
		h.offset++
	case "pgup":
		h.offset -= page
	case "pgdown":
		h.offset += page
	}
	h.offset = min(max(h.offset, 0), max(n-room, 0))
}

// ---- the screens before the run -------------------------------------------------

// The words help says about each screen before the run.
const (
	helpBeforeRun = "Nothing has been changed yet: nothing on these screens changes anything until you approve " +
		"the plan on the review. Ctrl+C exits."
	helpWelcome   = "Continue runs the read-only checks; Exit leaves. "
	helpPreflight = "The checks are read-only: they never install software or change Docker, the kernel or a " +
		"cluster. A fix is advice, or a change to the pending plan that runs the checks again; Recheck runs " +
		"them again as they are. Continue is unavailable while any check fails. "
	helpSwitch = "Switch changes the pending plan to the other footprint and runs every check again for it; " +
		"Keep leaves the plan as it is. "
	helpChoices = "Each change re-resolves the plan as the command line would, and runs the read-only checks " +
		"again for it. Continue shows the review; Back returns to the checks. "
	helpName = "Type the local kind cluster's name: Enter uses it and runs the checks again, Esc keeps the " +
		"name it had. ? is part of a name here, so F1 opens help. "
	helpDrawer = "Editors change the plan at once and run the checks again; every other flag from the command " +
		"line is carried as given. Esc returns to the choices. "
	helpPicker = "Choosing a context makes it the target and runs the checks against it. dcctl never creates " +
		"or deletes a context's cluster. Esc keeps the target it had. "
	helpReview = "Approve starts the install of the plan shown, and only once this screen has settled: a key " +
		"pressed before then does nothing. Opening, scrolling and closing help are keys too, so the screen " +
		"settles again after help closes. Back returns to the choices. "
)

// typingName reports whether the kind cluster's name is being typed, where ? is a
// character and only F1 opens help.
func (p *prerun) typingName() bool {
	return p.ch.picker == nil && p.screen == screenChoices && p.ch.naming != nil
}

// opensHelp reports whether k opens help on the screen up now.
func (p *prerun) opensHelp(k tea.KeyPressMsg) bool {
	if p.typingName() {
		return key.Matches(k, p.keys.helpF1)
	}
	return key.Matches(k, p.keys.help)
}

// helpKey takes every key while help is open, and the key that opens it. On the review
// the key has already been through the settle gate (prerun.update).
func (p *prerun) helpKey(k tea.KeyPressMsg) {
	switch {
	case !p.help.open:
		p.help = helpState{open: true}
	case key.Matches(k, p.keys.closeHelp):
		p.help = helpState{}
	case key.Matches(k, p.keys.helpScroll):
		_, list, room := p.helpParts()
		p.help.scroll(k.String(), len(list), room)
	}
}

// helpTitle names the screen help is open over.
func (p *prerun) helpTitle() string {
	switch {
	case p.ch.picker != nil:
		return pickerTitle
	case p.screen == screenWelcome:
		return welcomeTitle
	case p.typingName():
		return nameTitle
	case p.screen == screenChoices && p.ch.drawer:
		return drawerTitle
	case p.screen == screenChoices:
		return choicesTitle
	case p.screen == screenReview:
		return reviewTitle
	}
	return preflightTitle
}

// helpAbout is what help says the screen does.
func (p *prerun) helpAbout() string {
	switch {
	case p.ch.picker != nil:
		return helpPicker + helpBeforeRun
	case p.screen == screenWelcome:
		return helpWelcome + helpBeforeRun
	case p.typingName():
		return helpName + helpBeforeRun
	case p.screen == screenChoices && p.ch.drawer:
		return helpDrawer + helpBeforeRun
	case p.screen == screenChoices:
		return helpChoices + helpBeforeRun
	case p.screen == screenReview:
		return helpReview + helpBeforeRun
	case p.pf.confirm:
		return helpSwitch + helpBeforeRun
	}
	return helpPreflight + helpBeforeRun
}

// helpParts is help's title lines, its scrolled lines and the room they have, at the
// terminal's size.
func (p *prerun) helpParts() (head, list []string, room int) {
	f := p.frame()
	return p.helpPartsIn(f.inner, f.rows)
}

func (p *prerun) helpPartsIn(w, rows int) (head, list []string, room int) {
	t := p.theme
	head = []string{t.Bold.Render("Help") + " " + t.Glyphs.Dot + " " + p.helpTitle(), ""}
	list = result.HelpLines(t, p.footerKeys(), p.helpAbout(), w)
	return head, list, rows - len(head)
}

// helpLines is help's body in rows lines: its title, then its lines from the offset,
// those cut off above or below counted (see window).
func (p *prerun) helpLines(w, rows int) []string {
	head, list, room := p.helpPartsIn(w, rows)
	lines, offset := window(list, room, p.help.offset, p.theme.Glyphs.Ellipsis)
	p.help.offset = offset
	return append(head, lines...)
}

// ---- the progress screen ----------------------------------------------------------

// The words help says about the progress screen.
const (
	helpRunning = "The run goes on while help is open: help never pauses or stops it. Ctrl+C stops the run " +
		"safely and waits for it to settle; a second Ctrl+C exits at once. Esc never stops it. Nothing is " +
		"rolled back."
	helpStopping = "The run is stopping safely, and help does not change that. A second Ctrl+C exits at once, " +
		"without waiting. Nothing is rolled back."
	helpOver = "The run is over, and nothing is running while this screen is open. Leaving it never changes " +
		"dcctl's exit status, and dcctl prints a short summary when it exits."
	helpBars = " The overall bar counts finished stages, never a percentage, and a stage's bar is full only " +
		"once the stage has finished. Work with no known total shows that it is working, never a number."
	helpQuestion = "A question is waiting: close help to answer it."
	helpLogs     = " l opens the log pane; while it is open PgUp and PgDn scroll it, and f follows it again."
)

// helpKey takes every key while help is open, and the key that opens it.
func (m model) helpKey(k tea.KeyPressMsg) model {
	switch {
	case !m.help.open:
		m.help = helpState{open: true}
	case key.Matches(k, m.keys.closeHelp):
		m.help = helpState{}
	case key.Matches(k, m.keys.helpScroll):
		_, list, room := m.helpParts(newFrame(m.width, m.height))
		m.help.scroll(k.String(), len(list), room)
	}
	return m
}

// helpAbout is what help says the progress screen does now.
func (m model) helpAbout() string {
	var s string
	switch {
	case m.summary != nil:
		s = helpOver
	case m.stopping:
		s = helpStopping
	default:
		s = helpRunning
	}
	if m.summary == nil {
		s += helpBars
	}
	if m.logs != nil {
		s += helpLogs
	}
	return s
}

// helpParts is help's title lines, its scrolled lines and the room they have in f.
func (m model) helpParts(f frame) (head, list []string, room int) {
	rt := result.NewTheme(m.caps)
	title := "Help " + rt.Glyphs.Dot + " " + m.runWord() + " progress"
	if width(title) <= f.inner {
		title = rt.Bold.Render("Help") + strings.TrimPrefix(title, "Help")
	} else {
		title = truncate(title, f.inner, m.theme.g.ellipsis)
	}
	head = []string{title}
	if m.prompt != nil {
		for _, l := range wrap(helpQuestion, f.inner) {
			head = append(head, rt.Warn.Render(l))
		}
	}
	head = append(head, "")
	list = result.HelpLines(rt, m.keys.screen(m), m.helpAbout(), f.inner)
	return head, list, f.rows - len(head)
}

// helpBody is help in exactly f.rows lines, each checked as composed.
func (m model) helpBody(f frame) []string {
	head, list, room := m.helpParts(f)
	lines, _ := window(list, room, m.help.offset, m.theme.g.ellipsis)
	body := checkLines(m.redact, append(head, lines...))
	for i, l := range body {
		if width(l) > f.inner { // a terminal narrower than a key's row: the words, cut
			body[i] = truncate(installflow.Text(l).String(), f.inner, m.theme.g.ellipsis)
		}
	}
	for len(body) < f.rows {
		body = append(body, "")
	}
	return clip(body, f.rows)
}
