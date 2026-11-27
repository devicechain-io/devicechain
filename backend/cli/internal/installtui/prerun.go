// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/result"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// prerunOutcome is what the screens before the run decided.
type prerunOutcome uint8

const (
	// stay: still on the screens.
	stay prerunOutcome = iota
	// proceed: start the run, with the plan as it now stands.
	proceed
	// leave: the operator chose Exit. Nothing has been changed.
	leave
)

type prerunScreen uint8

const (
	screenWelcome prerunScreen = iota
	screenPreflight
	screenChoices
	screenReview
)

// Minimum size the screens before the run are drawn at. Below it they ask for a larger
// terminal and take no input but Ctrl+C: a choice made on a screen nobody can see is
// not a choice.
const (
	prerunMinWidth  = 60
	prerunMinHeight = 16
)

// prerun is the welcome, preflight and cluster-choice screens: everything shown before
// the run starts. It changes nothing anywhere. Its checks are the read-only preflight,
// and what it can change (the target, footprint, topology and the drawer's editors) is a
// change to the pending plan made through the command's own resolver.
type prerun struct {
	caps terminal.Capabilities
	// theme is the result screens' theme: one set of tokens and glyphs for every
	// screen of the installer.
	theme result.Theme
	keys  prerunKeys
	// redact is the hub's: every text the screens draw passes through it after
	// sanitising, so a secret the run knows is never drawn.
	redact func(string) string
	ctx    context.Context
	width  int
	height int

	facts    Facts
	settings []Setting
	contexts func() ([]string, error)
	replan   func(Edits) (Replanned, error)
	// edits are the changes made so far; the plan is the command line's request with
	// them applied.
	edits Edits
	// engine and runner are the plan as it stands: nil engine means the one the command
	// passed. runner is what the screen's checks run.
	engine installflow.Engine
	runner installflow.PreflightRunner
	// refusal is why the last change was refused; the plan stayed as it was.
	refusal string
	// review and command are the review screen's plan and its equivalent command,
	// quoted for shell, for the plan as it stands.
	review  Review
	command result.Command
	shell   result.Shell
	// plan counts the plans: every change the screens make is a new one. approval is
	// for the plan it was given on, and for no other.
	plan     int
	approval *approval
	// advisory is the plan's advisory checks, run after runner, on this screen only;
	// see Wizard.Advisory. A replan replaces it with the new plan's.
	advisory installflow.PreflightRunner

	// checks counts the screen's preflight runs still going; wait joins them.
	checks sync.WaitGroup

	screen  prerunScreen
	welcome int // the focused welcome action
	pf      preflightState
	ch      choicesState
	rv      reviewState
	// help is the help over whichever screen is up. It changes nothing under it.
	help helpState
}

func newPrerun(ctx context.Context, caps terminal.Capabilities, w *Wizard, runner installflow.PreflightRunner,
	redact func(string) string) *prerun {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	theme := result.NewTheme(caps)
	p := &prerun{caps: caps, theme: theme, keys: newPrerunKeys(theme.Glyphs),
		redact: redact, ctx: ctx, width: caps.Width, height: caps.Height,
		facts: w.Facts, settings: w.Settings, contexts: w.Contexts, replan: w.Replan, runner: runner,
		review: w.Review, command: w.Command, shell: w.Shell,
		advisory: w.Advisory}
	p.pf = newPreflightState()
	return p
}

// text is s as the screens may draw it: sanitised (escapes, controls and format
// characters gone, one line) and redacted.
func (p *prerun) text(s string) string { return p.redact(installflow.Text(s).String()) }

// safe is a payload's text as the screens may draw it.
func (p *prerun) safe(t installflow.SafeText) string { return p.redact(t.String()) }

func (p *prerun) resize(w, h int) { p.width, p.height = w, h }

// wait returns once every preflight run the screen started has returned. The runs
// end with the context they were given.
func (p *prerun) wait() { p.checks.Wait() }

// tooSmall is the layout below which nothing but a request to resize is shown.
func (p *prerun) tooSmall() bool { return p.width < prerunMinWidth || p.height < prerunMinHeight }

// update handles one message. Keys arrive here only while these screens are up, and
// never Ctrl+C, which the program's cancel path owns.
func (p *prerun) update(msg tea.Msg) (tea.Cmd, prerunOutcome) {
	switch msg := msg.(type) {
	case checkMsg:
		return p.checkResult(msg), stay
	case settleMsg:
		if p.screen == screenReview {
			return p.rv.settled(msg), stay
		}
		return nil, stay
	case tea.KeyPressMsg:
		// 🔴 THE REVIEW'S SETTLE GATE SEES EVERY KEY FIRST, before any guard below can drop
		// it: a key that will not reach the review's own handler (the terminal is too small,
		// help takes it) is input that does not act on the review, so it restarts the quiet
		// period and disarms a review that had settled (settleGate.interrupt). Where nothing
		// reads the terminal for us (a Windows console) this is the only stamp it gets; a key
		// that does reach the review is stamped and judged by settleGate.admit there.
		var gate tea.Cmd
		if p.screen == screenReview && (p.tooSmall() || p.help.open || p.opensHelp(msg)) {
			gate = p.rv.interrupt()
		}
		if p.tooSmall() {
			return gate, stay
		}
		if p.help.open || p.opensHelp(msg) {
			// 🔴 HELP TAKES THE KEY WHOLE: nothing under it moves, changes or answers.
			p.helpKey(msg)
			return gate, stay
		}
		if p.ch.picker != nil {
			return p.pickerKey(msg)
		}
		switch p.screen {
		case screenWelcome:
			return p.welcomeKey(msg)
		case screenChoices:
			return p.choicesKey(msg)
		case screenReview:
			return p.reviewKey(msg)
		}
		return p.preflightKey(msg)
	}
	return nil, stay
}

// ---- keys -------------------------------------------------------------------

// prerunKeys is every key the screens before the run answer. The footer is generated
// from the bindings active at the moment, so it cannot advertise a key that does
// nothing.
type prerunKeys struct {
	next, prev, up, down, enter, back, exit, cancel key.Binding
	confirm, keep                                   key.Binding
	// The cluster choices, the drawer, the picker and the name editor.
	nextField, choose, toggle, setting, pick, closeOverlay, apply, erase key.Binding
	// The review.
	scroll key.Binding
	// Help: ? or F1 opens it (F1 alone where ? is typed into a name), and it scrolls and
	// closes with its own keys.
	help, helpF1, closeHelp, helpScroll key.Binding
}

func newPrerunKeys(g result.Glyphs) prerunKeys {
	updown := "↑/↓"
	if g.Ellipsis != "…" { // an ASCII level
		updown = "up/down"
	}
	return prerunKeys{
		next:    key.NewBinding(key.WithKeys("tab", "right"), key.WithHelp("Tab", "action")),
		prev:    key.NewBinding(key.WithKeys("shift+tab", "left")),
		up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp(updown, "check")),
		down:    key.NewBinding(key.WithKeys("down", "j")),
		enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "select")),
		back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "back")),
		exit:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "exit")),
		cancel:  key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("Ctrl+C", "exit")),
		confirm: key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "switch")),
		keep:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "keep")),

		nextField:    key.NewBinding(key.WithKeys("tab", "right"), key.WithHelp("Tab", "next")),
		choose:       key.NewBinding(key.WithKeys("up", "down", "space"), key.WithHelp(updown+"/Space", "choose")),
		toggle:       key.NewBinding(key.WithKeys("space", "enter"), key.WithHelp("Space", "toggle")),
		setting:      key.NewBinding(key.WithKeys("up", "down"), key.WithHelp(updown, "setting")),
		pick:         key.NewBinding(key.WithKeys("up", "down"), key.WithHelp(updown, "context")),
		closeOverlay: key.NewBinding(key.WithKeys("esc"), key.WithHelp("Esc", "close")),
		apply:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("Enter", "use")),
		erase:        key.NewBinding(key.WithKeys("backspace"), key.WithHelp("Backspace", "erase")),

		scroll: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp(updown, "scroll")),

		help:       key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?/F1", "help")),
		helpF1:     key.NewBinding(key.WithKeys("f1"), key.WithHelp("F1", "help")),
		closeHelp:  key.NewBinding(key.WithKeys("esc", "?", "f1"), key.WithHelp("Esc", "close help")),
		helpScroll: key.NewBinding(key.WithKeys(helpScrollKeys...), key.WithHelp(updown, "scroll")),
	}
}

// active is the bindings to advertise now, in footer order: the screen's, or help's
// while help is open over it.
func (p *prerun) active() []key.Binding {
	k := p.keys
	switch {
	case p.tooSmall():
		return []key.Binding{k.cancel}
	case p.help.open:
		out := []key.Binding{k.closeHelp}
		if _, list, room := p.helpParts(); len(list) > room {
			out = append(out, k.helpScroll)
		}
		return append(out, k.cancel)
	}
	return p.footerKeys()
}

// footerKeys is the screen's footer with help closed: its own bindings, and help's key
// just before the way out. The screen's help lists exactly these.
func (p *prerun) footerKeys() []key.Binding {
	k := p.keys
	keys := p.screenKeys()
	// Help is offered on every screen, just before the way out.
	h := k.help
	if p.typingName() {
		h = k.helpF1
	}
	n := len(keys)
	return append(append(keys[:n-1:n-1], h), keys[n-1])
}

// screenKeys is the screen's own bindings, before help's, ending with Ctrl+C.
func (p *prerun) screenKeys() []key.Binding {
	k := p.keys
	switch {
	case p.screen == screenWelcome:
		return []key.Binding{k.enter, k.next, k.exit, k.cancel}
	case p.ch.picker != nil:
		return p.pickerActive()
	case p.screen == screenChoices:
		return p.choicesActive()
	case p.screen == screenReview:
		return p.reviewActive()
	case p.pf.confirm:
		return []key.Binding{k.confirm, k.keep, k.cancel}
	}
	out := []key.Binding{}
	if p.pf.checkCount() > 1 {
		out = append(out, k.up)
	}
	return append(out, k.next, k.enter, k.back, k.exit, k.cancel)
}

// footer is the active keys, on as many lines as the width needs: a key is never cut.
func (p *prerun) footer(w int) []string {
	sep := " " + p.theme.Glyphs.Dot + " "
	var lines []string
	cur := ""
	for _, b := range p.active() {
		h := b.Help()
		part := h.Key + " " + h.Desc
		switch {
		case cur == "":
			cur = part
		case cellWidth(cur)+cellWidth(sep)+cellWidth(part) <= w:
			cur += sep + part
		default:
			lines = append(lines, cur)
			cur = part
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	for i, l := range lines {
		lines[i] = p.theme.Dim.Render(truncCells(l, w, p.theme.Glyphs.Ellipsis))
	}
	return lines
}

// ---- chrome -----------------------------------------------------------------

// prerunFrame is the space one layout gives the body.
//
//	>= 80 columns: one outer border, the header in its top edge, a rule above the footer
//	60-79 columns: no outer border; the header, a rule, the body, a rule, the footer
//	below 60x16:   a request to resize, and nothing else
type prerunFrame struct {
	border      bool
	inner, rows int
}

func (p *prerun) frame() prerunFrame {
	if p.width >= 80 {
		return prerunFrame{border: true, inner: p.width - 4, rows: p.height - 4}
	}
	return prerunFrame{inner: p.width, rows: p.height - 4}
}

func (p *prerun) title() string {
	t := "DeviceChain " + p.theme.Glyphs.Dot + " dcctl install"
	if target := p.targetShort(); target != "" {
		t += " " + p.theme.Glyphs.Dot + " " + target
	}
	return t
}

func (p *prerun) targetShort() string {
	if p.facts.KubeContext != "" {
		return "context " + p.text(p.facts.KubeContext)
	}
	if p.facts.Cluster != "" {
		return "kind " + p.text(p.facts.Cluster)
	}
	return ""
}

// render draws the screen at exactly the terminal's size.
func (p *prerun) render() string {
	g := p.theme.Glyphs
	if p.tooSmall() {
		return p.renderTooSmall()
	}
	f := p.frame()
	footer := p.footer(f.inner)
	f.rows -= len(footer) - 1
	var body []string
	switch {
	case p.help.open:
		body = p.helpLines(f.inner, f.rows)
	case p.ch.picker != nil:
		body = p.pickerLines(f.inner, f.rows)
	case p.screen == screenWelcome:
		body = p.welcomeLines(f.inner, f.rows)
	case p.screen == screenChoices:
		body = p.choicesLines(f.inner, f.rows)
	case p.screen == screenReview:
		body = p.reviewLines(f.inner, f.rows)
	default:
		body = p.preflightLines(f.inner, f.rows)
	}
	body = fit(body, f.rows)
	version := p.text(p.facts.Version)
	out := make([]string, 0, p.height)
	if f.border {
		out = append(out, p.topEdge(p.width, version))
		for _, l := range body {
			out = append(out, g.V+" "+padCells(l, f.inner)+" "+g.V)
		}
		out = append(out, g.TeeL+strings.Repeat(g.H, p.width-2)+g.TeeR)
		for _, l := range footer {
			out = append(out, g.V+" "+padCells(l, f.inner)+" "+g.V)
		}
		out = append(out, g.BL+strings.Repeat(g.H, p.width-2)+g.BR)
	} else {
		left := truncCells(p.title(), f.inner-cellWidth(version)-1, g.Ellipsis)
		head := p.theme.Bold.Render(left)
		if version != "" {
			head = padCells(head, f.inner-cellWidth(version)) + p.theme.Dim.Render(version)
		}
		out = append(out, head, strings.Repeat(g.H, p.width))
		out = append(out, body...)
		out = append(out, strings.Repeat(g.H, p.width))
		out = append(out, footer...)
	}
	return strings.Join(p.checked(out), "\n")
}

// checked is the screen's lines as they may be drawn.
//
// 🔴 EVERY LINE IS CHECKED AS COMPOSED, NOT ONLY ITS PARTS. Each value on these
// screens has been sanitised and redacted on its own (text, safe), but a label, its
// padding and a separator are text too: "Target" joined to a clean context name can be
// a registered value that neither part is. A line the hub's redaction would change is
// withheld whole, as the progress and result screens withhold theirs; and the marker
// itself is checked, so nothing leaves unchecked.
func (p *prerun) checked(lines []string) []string { return checkLines(p.redact, lines) }

// checkLines is lines as they may be drawn: each one, as composed and with its styling
// removed, is withheld whole when redact would change it (see prerun.checked). A nil
// redact changes nothing.
func checkLines(redact func(string) string, lines []string) []string {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	for i, l := range lines {
		l = strings.TrimRight(l, " ")
		if plain := installflow.Text(l).String(); redact(plain) != plain {
			l = withheld
			if redact(l) != l {
				l = ""
			}
		}
		lines[i] = l
	}
	return lines
}

// topEdge is the border's top edge with the title at the left and the version at the
// right, both cut to fit.
func (p *prerun) topEdge(w int, version string) string {
	g := p.theme.Glyphs
	room := w - 4 // the corners and one rule glyph each side
	right := ""
	if version != "" && cellWidth(version)+2 < room/2 {
		right = " " + version + " "
	}
	left := " " + truncCells(p.title(), room-cellWidth(right)-2, g.Ellipsis) + " "
	fill := w - 2 - 2 - cellWidth(left) - cellWidth(right)
	if fill < 0 {
		fill = 0
	}
	return g.TL + g.H + p.theme.Bold.Render(left) + strings.Repeat(g.H, fill) + p.theme.Dim.Render(right) + g.H + g.TR
}

func (p *prerun) renderTooSmall() string {
	msg := fmt.Sprintf("This terminal is %dx%d. The installer needs at least %dx%d: make the window larger, "+
		"or press Ctrl+C and run the same command with --ui=plain.", p.width, p.height, prerunMinWidth, prerunMinHeight)
	w := p.width
	if w < 10 {
		w = 10
	}
	lines := append([]string{p.theme.Bold.Render("DeviceChain")}, wrap(msg, w)...)
	return strings.Join(p.checked(fit(lines, p.height)), "\n")
}

// ---- text layout --------------------------------------------------------------

// stack lays a screen's body out in rows lines: the head, the list scrolled to keep
// lines [from, to] in view, and the tail, which holds the actions.
//
// 🔴 THE ACTIONS ARE NEVER WHAT GIVES WAY. Their keys are live, so an action that is
// not on screen could still be chosen. When space is short the blank separators go
// first, then the list scrolls (counted, never silently), then the head gives way to
// keep the list two lines (its focused row and a count), and if even the head and
// the tail do not fit, the body keeps its END: the head and the top of the tail are
// cut, never the actions.
func stack(head, list, tail []string, rows, from, to int, ellipsis string) []string {
	if rows <= 0 {
		return nil
	}
	if rows-len(head)-len(tail) < 3 {
		head, tail = dropBlank(head), dropBlank(tail)
	}
	room := rows - len(head) - len(tail)
	if room < 2 && len(list) > room && len(head) > 0 {
		// The list keeps room for its focused row and a marker counting the rest: the
		// head (the screen's title) gives way first.
		head = nil
		room = rows - len(tail)
	}
	body := append(append(append([]string(nil), head...), scroll(list, room, from, to, ellipsis)...), tail...)
	if len(body) > rows {
		body = body[len(body)-rows:]
	}
	return body
}

func dropBlank(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// hang wraps text into a block whose first line starts with lead and whose other lines
// are indented to line up under the text. lead may be styled; it is measured by display
// width.
func hang(lead, text string, w int) []string {
	lw := cellWidth(lead)
	lines := wrap(text, w-lw)
	out := make([]string, len(lines))
	for i, l := range lines {
		if i == 0 {
			out[i] = lead + l
		} else {
			out[i] = strings.Repeat(" ", lw) + l
		}
	}
	return out
}

// fit pads or cuts lines to exactly n.
func fit(lines []string, n int) []string {
	if n < 0 {
		n = 0
	}
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

// actionBar draws a row of actions: the focused one with the focus glyph and the
// inverse highlight, an unavailable one with the word saying so.
func (p *prerun) actionBar(labels []string, enabled []bool, focus int, w int) []string {
	var parts []string
	for i, l := range labels {
		if !enabled[i] {
			l += " (unavailable)"
		}
		if i == focus {
			parts = append(parts, p.theme.Glyphs.Focus+" "+p.theme.Focus.Render(l))
		} else if !enabled[i] {
			parts = append(parts, "  "+p.theme.Dim.Render(l))
		} else {
			parts = append(parts, "  "+l)
		}
	}
	line := strings.Join(parts, "  ")
	if cellWidth(line) <= w {
		return []string{line}
	}
	// Too narrow for one row: one action per line, so none is cut.
	return parts
}

// ---- cells -------------------------------------------------------------------

// cellWidth is a string's display width in cells.
func cellWidth(s string) int { return lipgloss.Width(s) }

// truncCells cuts plain text to w cells, marking the cut. It is for text that has not
// been styled yet; styling happens after.
func truncCells(s string, w int, ellipsis string) string {
	if w <= 0 {
		return ""
	}
	if cellWidth(s) <= w {
		return s
	}
	ew := cellWidth(ellipsis)
	if ew >= w {
		ellipsis, ew = "", 0
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := cellWidth(string(r))
		if used+rw > w-ew {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + ellipsis
}

// padCells right-pads styled text to w cells, measured by display width.
func padCells(s string, w int) string {
	if d := w - cellWidth(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
