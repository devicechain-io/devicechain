// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Action is what the operator chose on a result screen.
type Action uint8

const (
	// ActionExit leaves the program. It never changes the exit status.
	ActionExit Action = iota
	// ActionRerun asks for a fresh run of the same request, through review. It is
	// offered only once the previous run has settled.
	ActionRerun
)

// Done is sent once, when the operator leaves the screen.
type Done struct{ Action Action }

// settledMsg says the previous run has settled, so a re-run may be offered.
type settledMsg struct{}

type keyMap struct {
	next, prev, activate, exit, help, close key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		next:     key.NewBinding(key.WithKeys("tab", "right"), key.WithHelp("Tab", "move")),
		prev:     key.NewBinding(key.WithKeys("shift+tab", "left"), key.WithHelp("Shift+Tab", "move back")),
		activate: key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("Enter", "select")),
		exit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "exit")),
		help:     key.NewBinding(key.WithKeys("?", "f1"), key.WithHelp("?", "help")),
		close:    key.NewBinding(key.WithKeys("esc", "?", "f1"), key.WithHelp("Esc", "close help")),
	}
}

type button struct {
	label   string
	action  Action
	waiting string // why it cannot be chosen yet; "" when it can
}

// Model is the failure or success screen for one finished run.
type Model struct {
	in     Input
	caps   terminal.Capabilities
	theme  theme
	keys   keyMap
	width  int
	height int

	focus   int
	help    bool
	settled bool
	done    bool
}

// New returns the result screen for a finished run. The screen it draws follows
// the summary alone: success only for a successful verdict.
func New(in Input, caps terminal.Capabilities) *Model {
	return &Model{in: in, caps: caps, theme: newTheme(caps), keys: newKeyMap(),
		width: caps.Width, height: caps.Height}
}

// Init waits, off the event loop, for the previous run to settle.
func (m *Model) Init() tea.Cmd {
	if m.in.Settled == nil || !m.offersRerun() {
		return nil
	}
	settled := m.in.Settled
	return func() tea.Msg {
		<-settled
		return settledMsg{}
	}
}

func (m *Model) offersRerun() bool {
	k := kindOf(m.in.Summary)
	return m.in.Settled != nil && (k == kindFailed || k == kindStopped)
}

// settledNow reads the admission itself, not a copy of what it said.
func (m *Model) settledNow() bool {
	if m.in.Settled == nil {
		return false
	}
	select {
	case <-m.in.Settled:
		return true
	default:
		return false
	}
}

func (m *Model) buttons() []button {
	switch kindOf(m.in.Summary) {
	case kindSucceeded, kindPlanned:
		return []button{{label: "Done", action: ActionExit}}
	}
	bs := []button{{label: "Exit", action: ActionExit}}
	if m.offersRerun() {
		b := button{label: "Review for re-run", action: ActionRerun}
		if !m.settled {
			b.waiting = "waiting"
		}
		bs = append(bs, b)
	}
	return bs
}

// finish reports the choice, once.
func (m *Model) finish(a Action) tea.Cmd {
	if m.done {
		return nil
	}
	m.done = true
	return func() tea.Msg { return Done{Action: a} }
}

// Update handles keys, sizes and the settle notice.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case settledMsg:
		m.settled = true
	case tea.KeyPressMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	if key.Matches(k, m.keys.exit) {
		return m.finish(ActionExit)
	}
	if m.help {
		if key.Matches(k, m.keys.close) {
			m.help = false
		}
		return nil
	}
	bs := m.buttons()
	switch {
	case key.Matches(k, m.keys.help):
		m.help = true
	case key.Matches(k, m.keys.next):
		m.focus = (m.focus + 1) % len(bs)
	case key.Matches(k, m.keys.prev):
		m.focus = (m.focus + len(bs) - 1) % len(bs)
	case key.Matches(k, m.keys.activate):
		b := bs[min(m.focus, len(bs)-1)]
		if b.action == ActionRerun && (b.waiting != "" || !m.settledNow()) {
			return nil // 🔴 never a fresh run over one that has not settled
		}
		return m.finish(b.action)
	}
	return nil
}

// activeKeys is every key the screen answers now, in footer order.
func (m *Model) activeKeys() []key.Binding {
	if m.help {
		return []key.Binding{m.keys.close, m.keys.exit}
	}
	var out []key.Binding
	if len(m.buttons()) > 1 {
		out = append(out, m.keys.next)
	}
	return append(out, m.keys.activate, m.keys.help, m.keys.exit)
}

// View draws the screen. The router decides the screen mode.
func (m *Model) View() tea.View { return tea.NewView(m.render()) }

// layout is the layout ladder for the current size.
func (m *Model) layout() terminal.Layout {
	switch w, h := m.width, m.height; {
	case w <= 0 || h <= 0:
		return terminal.SingleColumn
	case w < 60 || h < 16:
		return terminal.Minimal
	case w >= 100 && h >= 30:
		return terminal.Full
	case w >= 80:
		return terminal.SingleColumn
	}
	return terminal.Compact
}

func (m *Model) size() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	return w, h
}

const labelWidth = 12

// row is one labelled fact. A command row is drawn in the accent colour.
type row struct {
	label, value string
	command      bool
	// words are a command's words, when the serialiser recorded them.
	words []string
	style *lipgloss.Style
	// drop ranks the row for leaving out when the screen is short: 0 never, and
	// the highest goes first. The exit summary keeps what a short screen drops.
	drop int
	// shrink lets the row be cut to fewer lines, ending in an ellipsis, once
	// nothing droppable is left. The cause is: dcctl prints it in full on exit.
	shrink bool
}

// Drop ranks.
const (
	keep = iota
	dropLast
	dropLate
	dropSoon
	dropFirst
	dropBlank
)

func (m *Model) statusWord() (string, lipgloss.Style) {
	switch kindOf(m.in.Summary) {
	case kindSucceeded:
		return "DONE", m.theme.ok
	case kindPlanned:
		return "PLANNED", m.theme.ok
	case kindStopped:
		return "STOPPED", m.theme.warn
	}
	return "FAILED", m.theme.fail
}

func (m *Model) rows() []row {
	in := m.in
	var rs []row
	switch kindOf(in.Summary) {
	case kindSucceeded, kindPlanned:
		rs = append(rs,
			row{label: "Target", value: in.target()},
			row{label: "Shape", value: in.Facts.shape()},
			row{label: "TLS", value: onOff(in.Facts.TLS), drop: dropLate},
			row{label: "Monitoring", value: onOff(in.Facts.Monitoring), drop: dropLate},
			row{label: "Backups", value: in.Facts.backups(), drop: dropLast},
			row{label: "Connections", value: in.Facts.budget(), drop: dropLate},
		)
		for _, c := range in.Summary.Credentials {
			rs = append(rs, row{label: "Credentials", value: credentialWords(c), drop: dropLast})
		}
		rs = append(rs, row{label: "Console", value: "none for a cluster: it comes with an instance", drop: dropSoon},
			row{drop: dropBlank})
		for i, n := range in.nextSteps() {
			label := "Next"
			if i > 0 {
				label = ""
			}
			rs = append(rs, row{label: label, value: n, command: true})
		}
		rs = append(rs, m.commandRows("Command")...)
		return rs
	}
	if at := in.failedAt(); at != "" {
		rs = append(rs, row{label: "Stage", value: at})
	}
	rs = append(rs,
		row{label: "Cause", value: in.Cause(), shrink: true},
		row{label: "Changes", value: in.changes()},
		row{label: "Claim", value: claimWords(in.Summary.Claim), drop: dropLast},
		row{label: "Record", value: recordWords(in.Summary.InstallRecord), drop: dropSoon},
		row{drop: dropBlank},
		row{label: "Fix", value: in.fix()},
	)
	if c := in.inspect(); c != "" {
		rs = append(rs, row{label: "Inspect", value: c, command: true, drop: dropLate})
	}
	rs = append(rs, m.commandRows("Re-run")...)
	if m.offersRerun() && !m.settled {
		dim := m.theme.dim
		rs = append(rs, row{value: "Review for re-run: waiting for the last run to settle.", style: &dim, drop: dropFirst})
	}
	return rs
}

func (m *Model) commandRows(label string) []row {
	c := m.in.Equivalent
	if c.Line == "" {
		return nil
	}
	rs := []row{{label: label, value: c.display(m.in.Shell), command: true, words: c.words(m.in.Shell)}}
	if c.Template {
		dim := m.theme.dim
		rs = append(rs, row{value: "template: supply the redacted credentials privately", style: &dim, drop: dropFirst})
	}
	return rs
}

// helpLines is the overlay: every key the screen answers, generated from the
// same bindings as the footer, and what leaving the screen does.
func (m *Model) helpLines(width int) []string {
	keys := []key.Binding{m.keys.next, m.keys.prev, m.keys.activate, m.keys.exit, m.keys.close}
	if len(m.buttons()) < 2 {
		keys = []key.Binding{m.keys.activate, m.keys.exit, m.keys.close}
	}
	out := []string{paint(m.theme.bd, "Help"), ""}
	for _, k := range keys {
		h := k.Help()
		out = append(out, paint(m.theme.acc, padRight(h.Key, labelWidth))+h.Desc)
	}
	out = append(out, "")
	text := "The run is over; nothing is running while this screen is open. Leaving it never " +
		"changes dcctl's exit status, and dcctl prints a short summary when it exits."
	if k := kindOf(m.in.Summary); k == kindFailed || k == kindStopped {
		text += " Review for re-run starts a fresh run of the same request through review, " +
			"only once the last run has settled. Nothing is rolled back."
	}
	return append(out, wrap(text, width)...)
}

// bodyLines draws the title and the rows in at most budget lines, leaving out
// the least needed rows first, then shortening the cause, then cutting.
func (m *Model) bodyLines(width, budget int) []string {
	word, style := m.statusWord()
	lines := []string{paint(style, word) + "  " + paint(m.theme.bd, m.in.headline()), ""}
	if m.help {
		return clip(append(lines, m.helpLines(width)...), budget, width, m.theme)
	}
	rows := m.rows()
	vw := width - labelWidth
	wrapped := make([][]string, len(rows))
	total := len(lines)
	for i, r := range rows {
		wrapped[i] = []string{""}
		if r.value != "" {
			if r.command {
				wrapped[i] = wrapCommand(m.commandWords(r.value, r.words), vw)
			} else {
				wrapped[i] = wrap(m.in.shown(r.value), vw)
			}
		}
		total += len(wrapped[i])
	}
	for total > budget {
		pick := -1
		for i, r := range rows {
			if wrapped[i] != nil && r.drop > keep && (pick < 0 || r.drop >= rows[pick].drop) {
				pick = i
			}
		}
		if pick < 0 {
			break
		}
		total -= len(wrapped[pick])
		wrapped[pick] = nil
	}
	for i, r := range rows {
		if total > budget && r.shrink && len(wrapped[i]) > 1 {
			keepN := max(1, len(wrapped[i])-(total-budget))
			total -= len(wrapped[i]) - keepN
			cut := wrapped[i][:keepN]
			cut[keepN-1] = truncate(cut[keepN-1], vw-len(m.ellipsis())) + m.ellipsis()
			wrapped[i] = cut
		}
	}
	for i, r := range rows {
		if wrapped[i] == nil {
			continue
		}
		st := lipgloss.NewStyle()
		switch {
		case r.style != nil:
			st = *r.style
		case r.command:
			st = m.theme.acc
		}
		for n, l := range wrapped[i] {
			label := ""
			if n == 0 {
				label = r.label
			}
			if label == "" && l == "" {
				lines = append(lines, "")
				continue
			}
			// 🔴 The label column and the value are checked as ONE line: the label
			// and its padding, joined to a clean value, can be a registered one.
			lab := padRight(label, labelWidth)
			if plain := lab + l; m.in.shown(plain) != plain {
				l = withheld
				if fb := lab + l; m.in.shown(fb) != fb {
					lab = padRight("", labelWidth)
				}
			}
			lines = append(lines, paint(m.theme.dim, lab)+paint(st, l))
		}
	}
	return clip(lines, budget, width, m.theme)
}

func (m *Model) ellipsis() string {
	if m.theme.g.focus == ">" {
		return "..."
	}
	return "…"
}

func (m *Model) buttonLine() string {
	var parts []string
	for i, b := range m.buttons() {
		label := "[ " + b.label + " ]"
		if b.waiting != "" {
			label = "[ " + b.label + ": " + b.waiting + " ]"
		}
		switch {
		case i == m.focus%len(m.buttons()) && !m.help:
			parts = append(parts, m.theme.g.focus+" "+paint(m.theme.focus, label))
		case b.waiting != "":
			parts = append(parts, "  "+paint(m.theme.dim, label))
		default:
			parts = append(parts, "  "+label)
		}
	}
	return strings.Join(parts, " ")
}

func (m *Model) footer() string {
	var parts []string
	for _, k := range m.activeKeys() {
		h := k.Help()
		parts = append(parts, h.Key+" "+h.Desc)
	}
	return paint(m.theme.dim, strings.Join(parts, " "+m.theme.g.dot+" "))
}

func (m *Model) header(width int) string {
	parts := []string{"dcctl install"}
	if c := m.in.contextName(); c != "" {
		parts = append(parts, m.in.shown(c))
	}
	if m.in.Version != "" {
		parts = append(parts, m.in.Version)
	}
	// The JOINED line is checked as it is drawn, truncation included: the context,
	// the separator and the version can make a registered value none of them is.
	sep := " " + m.theme.g.dot + " "
	line := m.in.line(truncate(strings.Join(parts, sep), width), truncate(parts[0]+sep+withheld, width))
	return paint(m.theme.bd, truncate(line, width))
}

func (m *Model) render() string {
	w, h := m.size()
	switch m.layout() {
	case terminal.Minimal:
		return m.renderMinimal(w, h)
	case terminal.Compact:
		return m.renderFrame(w, h, false)
	}
	return m.renderFrame(w, h, true)
}

// renderFrame draws the header, the body, the actions and the footer, inside one
// outer border or, at compact widths, with none.
func (m *Model) renderFrame(w, h int, border bool) string {
	g := m.theme.g
	inner := w - 2 // one column of margin each side
	if border {
		inner = w - 4
	}
	fixed := 5 // header, rule, rule, actions, footer
	if border {
		fixed += 2
	}
	body := m.bodyLines(inner, h-fixed-1)
	content := []string{m.header(inner), "\x00rule"}
	content = append(content, body...)
	content = append(content, "", "\x00rule", m.buttonLine(), m.footer())

	var out []string
	if border {
		out = append(out, g.tl+strings.Repeat(g.h, w-2)+g.tr)
	}
	for _, l := range content {
		switch {
		case l == "\x00rule" && border:
			out = append(out, g.teeL+strings.Repeat(g.h, w-2)+g.teeR)
		case l == "\x00rule":
			out = append(out, strings.Repeat(g.h, w))
		case border:
			out = append(out, g.v+" "+padRight(l, inner)+" "+g.v)
		default:
			out = append(out, " "+l)
		}
	}
	if border {
		out = append(out, g.bl+strings.Repeat(g.h, w-2)+g.br)
	}
	return strings.Join(out, "\n")
}

// renderMinimal is the status block for a terminal under 60x16: the verdict,
// where it stopped, the command to run next, and how to leave.
func (m *Model) renderMinimal(w, h int) string {
	word, style := m.statusWord()
	// One column is left free where there is one to spare; nothing here is ever
	// wider than the terminal, or taller.
	inner := 1
	if w > 1 {
		inner = w - 1
	}
	title := paint(style, truncate(word, inner))
	if rest := inner - lipgloss.Width(word) - 1; rest > 0 {
		title += " " + paint(m.theme.bd, truncate(m.in.headline(), rest))
	}
	if h <= 1 {
		return title
	}
	var lines []string
	var next []string
	switch kindOf(m.in.Summary) {
	case kindSucceeded, kindPlanned:
		next = wrapCommand(append([]string{"Next:"}, m.commandWords(m.in.nextSteps()[0], nil)...), inner)
	default:
		if at := m.in.failedAt(); at != "" {
			lines = append(lines, wrap("At: "+at, inner)...)
		}
		next = wrap("Fix the cause, then re-run the same command.", inner)
	}
	lines = append(lines, next...)
	// Each line is checked as it is drawn: "Next:" or "At:" joined to a clean
	// value can be a registered one.
	for i, l := range lines {
		lines[i] = m.in.line(l, "")
	}
	// The verdict and the way out always show; what is between them fits the rest.
	if h == 2 {
		lines = nil
	} else {
		lines = clip(lines, h-2, inner, m.theme)
	}
	lines = append(append([]string{title}, lines...), paint(m.theme.dim, truncate("q exit "+m.theme.g.dot+" a summary is printed on exit", inner)))
	return strings.Join(lines, "\n")
}

// clip keeps at most n lines, saying how many it left out.
func clip(lines []string, n, width int, t theme) []string {
	if n < 1 {
		n = 1
	}
	if len(lines) <= n {
		return lines
	}
	more := len(lines) - n + 1
	out := append([]string(nil), lines[:n-1]...)
	note := "… " + strconv.Itoa(more) + " more lines; dcctl prints a summary on exit"
	if t.g.focus == ">" {
		note = "... " + strconv.Itoa(more) + " more lines; dcctl prints a summary on exit"
	}
	return append(out, paint(t.dim, truncate(note, width)))
}

// commandWords are a command's words as they may be drawn: the recorded ones,
// or those read from its quoting, re-checked against redaction as one line.
func (m *Model) commandWords(line string, words []string) []string {
	if words == nil {
		words = splitCommand(line, m.in.Shell)
	}
	joined := strings.Join(words, " ")
	if s := m.in.shown(joined); s != joined {
		return splitCommand(s, m.in.Shell)
	}
	return words
}

// wrap breaks plain text at spaces to width cells, hard-breaking only a word
// longer than a line. A hyphen is never a break, since a flag split at one reads
// as two flags, and the spaces kept on a line are the text's own: a name with
// two spaces in it is drawn with two.
func wrap(s string, width int) []string {
	var words, seps []string // seps[i] is the run of spaces before words[i]
	sep := ""
	for _, part := range strings.Split(s, " ") {
		if part == "" {
			sep += " "
			continue
		}
		words, seps = append(words, part), append(seps, sep)
		sep = " "
	}
	if len(words) > 0 && seps[0] == " " {
		seps[0] = ""
	}
	return fill(words, seps, width)
}

// wrapCommand lays a command's words out on lines, breaking only between two
// words and keeping every byte of each: whitespace inside a quoted argument is
// part of it, so it is never collapsed or broken at. A word longer than a line
// is cut across lines, still byte for byte.
func wrapCommand(words []string, width int) []string {
	seps := make([]string, len(words))
	for i := 1; i < len(seps); i++ {
		seps[i] = " "
	}
	return fill(words, seps, width)
}

// splitCommand finds the words of a quoted command line, for a line that came
// without them. It follows the shell's quoting: single quotes take everything up
// to the next single quote (PowerShell's doubled ” reads as a close and a
// reopen, which is the same word); inside double quotes, and outside any quote,
// the shell's escape character (POSIX backslash, PowerShell backtick) takes the
// next character with it, so an escaped quote never ends a word.
func splitCommand(s string, shell Shell) []string {
	esc := '\\'
	if shell == PowerShell {
		esc = '`'
	}
	var words []string
	var word strings.Builder
	quote := rune(0)
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			}
		case r == esc:
			escaped = true
		case quote == '"':
			if r == '"' {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ':
			if word.Len() > 0 {
				words = append(words, word.String())
				word.Reset()
			}
			continue
		}
		word.WriteRune(r)
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}
	return words
}

// fill puts words on lines of at most width cells, each word after its own
// separator, a line break standing in for the separator it falls on.
func fill(words, seps []string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	cur := ""
	for i, a := range words {
		switch {
		case cur == "" && len(out) == 0:
			cur = seps[i] + a // a leading separator stays on the first line
		case cur == "":
			cur = a
		case lipgloss.Width(cur+seps[i]+a) <= width:
			cur += seps[i] + a
		default:
			out = append(out, cur)
			cur = a
		}
		for lipgloss.Width(cur) > width {
			head := truncate(cur, width)
			if head == "" {
				// A rune wider than the whole line (a wide character in one
				// column) cannot be drawn without overflowing it: it is left out.
				_, n := utf8.DecodeRuneInString(cur)
				cur = cur[n:]
				continue
			}
			out = append(out, head)
			cur = cur[len(head):]
		}
	}
	if cur != "" || len(out) == 0 {
		out = append(out, cur)
	}
	return out
}

// truncate cuts plain text to w cells.
func truncate(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > w {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

// padRight pads styled text to w cells, measured by display width.
func padRight(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
