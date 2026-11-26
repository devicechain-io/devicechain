// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installtui/brand"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// bsTheme draws the bootstrap screens for one terminal. Every screen is composed
// in truecolor and then written once through the level's profile: L3 and L2
// downsample, and L1 (NO_COLOR) carries no styling at all, so every state the
// screens show is in their words and markers, never in colour alone.
type bsTheme struct {
	level   terminal.Level
	unicode bool
	width   int
	height  int
}

func newBSTheme(caps terminal.Capabilities) bsTheme {
	return bsTheme{level: caps.Level, unicode: caps.Unicode && caps.Level != terminal.L2,
		width: caps.Width, height: caps.Height}
}

// profile is what the composed screen is written through.
func (t bsTheme) profile() colorprofile.Profile {
	switch t.level {
	case terminal.L4:
		return colorprofile.TrueColor
	case terminal.L3:
		return colorprofile.ANSI256
	case terminal.L2:
		return colorprofile.ANSI
	}
	return colorprofile.NoTTY // L1: words only, no bold or faint either
}

// layout is the layout ladder for the current size.
func (t bsTheme) layout() terminal.Layout {
	switch {
	case t.width <= 0 || t.height <= 0:
		return terminal.UnknownLayout
	case t.width < 60 || t.height < 16:
		return terminal.Minimal
	case t.width >= 100 && t.height >= 30:
		return terminal.Full
	case t.width >= 80:
		return terminal.SingleColumn
	}
	return terminal.Compact
}

// glyphs, by whether the terminal can draw Unicode.
func (t bsTheme) focusMark() string {
	if t.unicode {
		return "›"
	}
	return ">"
}

func (t bsTheme) caret() string {
	if t.unicode {
		return "▏"
	}
	return "|"
}

func (t bsTheme) dot() string {
	if t.unicode {
		return " · "
	}
	return " - "
}

func (t bsTheme) ruleChar() string {
	if t.unicode {
		return "─"
	}
	return "-"
}

func (t bsTheme) check(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

func (t bsTheme) radio(on bool) string {
	if on {
		return "(*)"
	}
	return "( )"
}

var (
	bsChain = lipgloss.Color(brand.ChainColor)
	bsWarn  = lipgloss.Color("#E0A030")
	bsGood  = lipgloss.Color("#4CB782")
	bsBad   = lipgloss.Color("#E5484D")
)

type bsRole uint8

const (
	rolePlain bsRole = iota
	roleTitle
	roleLabel
	roleDim
	roleWarn
	roleGood
	roleBad
	roleFocus
	roleSecret
)

// style is a role's look. It is always truecolor here; the final write through
// the level's profile is what makes it fit the terminal.
func (bsTheme) style(r bsRole) lipgloss.Style {
	s := lipgloss.NewStyle()
	var fg color.Color
	switch r {
	case roleTitle:
		s, fg = s.Bold(true), bsChain
	case roleLabel:
		s = s.Bold(true)
	case roleDim:
		s = s.Faint(true)
	case roleWarn:
		s, fg = s.Bold(true), bsWarn
	case roleGood:
		fg = bsGood
	case roleBad:
		s, fg = s.Bold(true), bsBad
	case roleFocus:
		s = s.Reverse(true)
	case roleSecret:
		s = s.Bold(true)
	}
	if fg != nil {
		s = s.Foreground(fg)
	}
	return s
}

func (t bsTheme) paint(r bsRole, s string) string {
	if r == rolePlain || s == "" {
		return s
	}
	return t.style(r).Render(s)
}

// wrap breaks text into lines at most width cells wide, on spaces where it can
// and inside a word only when the word alone is wider than the line (a path).
func wrap(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for lipgloss.Width(word) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				cut := cutAt(word, width)
				out = append(out, word[:cut])
				word = word[cut:]
			}
			switch {
			case line == "":
				line = word
			case lipgloss.Width(line)+1+lipgloss.Width(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// cutAt is the byte index of the longest prefix of s at most width cells wide.
func cutAt(s string, width int) int {
	w := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > width {
			return max(i, 1)
		}
		w += rw
	}
	return len(s)
}

// para is text wrapped to width, every line in role r, each line prefixed by indent.
func (t bsTheme) para(r bsRole, indent string, text string, width int) []string {
	lines := wrap(text, width-lipgloss.Width(indent))
	for i, l := range lines {
		lines[i] = indent + t.paint(r, l)
	}
	return lines
}

// field is "label  value", the value wrapped under itself when it is long.
func (t bsTheme) field(label, value string, labelWidth, width int) []string {
	pad := strings.Repeat(" ", max(0, labelWidth-lipgloss.Width(label)))
	head := "  " + t.paint(roleLabel, label) + pad + "  "
	indent := strings.Repeat(" ", 2+labelWidth+2)
	vals := wrap(value, width-len(indent))
	out := []string{head + vals[0]}
	for _, v := range vals[1:] {
		out = append(out, indent+v)
	}
	return out
}

// footer is the key help line, generated from the bindings that are active now,
// so it never advertises a key that does nothing.
func (t bsTheme) footer(bindings []key.Binding, width int) []string {
	var parts []string
	for _, b := range bindings {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		parts = append(parts, h.Key+" "+h.Desc)
	}
	return t.para(roleDim, "", strings.Join(parts, t.dot()), width)
}

// bsScreen is one screen's content before the chrome is drawn around it.
type bsScreen struct {
	title  string
	body   []string
	keys   []key.Binding
	before bool // a screen shown before the run starts: too small a terminal asks for a resize
}

// contentWidth is the width a screen's body is wrapped to.
func (t bsTheme) contentWidth() int {
	switch t.layout() {
	case terminal.Full, terminal.SingleColumn:
		return t.width - 4 // the border and one space either side
	case terminal.UnknownLayout:
		return 76
	}
	return max(t.width, 20)
}

// frame draws the persistent chrome around a screen and writes it through the
// level's profile.
func (t bsTheme) frame(header string, s bsScreen) string {
	w := t.contentWidth()
	if t.layout() == terminal.Minimal && s.before {
		return t.render(strings.Join(t.para(rolePlain, "", "This terminal is too small for the bootstrap screens "+
			"(at least 60x16). Make it larger, or run the same command with --ui=plain.", max(t.width, 20)), "\n"))
	}
	rule := t.paint(roleDim, strings.Repeat(t.ruleChar(), w))
	var lines []string
	lines = append(lines, t.para(rolePlain, "", header, w)...)
	lines = append(lines, rule, t.paint(roleTitle, s.title), "")
	lines = append(lines, s.body...)
	lines = append(lines, "", rule)
	lines = append(lines, t.footer(s.keys, w)...)
	switch t.layout() {
	case terminal.Full, terminal.SingleColumn:
		lines = t.border(lines, w)
	}
	return t.render(strings.Join(lines, "\n"))
}

// border puts the one outer border around lines w cells wide.
func (t bsTheme) border(lines []string, w int) []string {
	tl, tr, bl, br, h, v := "╭", "╮", "╰", "╯", "─", "│"
	if !t.unicode {
		tl, tr, bl, br, h, v = "+", "+", "+", "+", "-", "|"
	}
	out := []string{tl + strings.Repeat(h, w+2) + tr}
	for _, l := range lines {
		out = append(out, v+" "+l+strings.Repeat(" ", max(0, w-lipgloss.Width(l)))+" "+v)
	}
	return append(out, bl+strings.Repeat(h, w+2)+br)
}

// render writes a composed screen through the level's profile.
func (t bsTheme) render(s string) string {
	var b strings.Builder
	w := &colorprofile.Writer{Forward: &b, Profile: t.profile()}
	_, _ = w.WriteString(s)
	return b.String()
}
