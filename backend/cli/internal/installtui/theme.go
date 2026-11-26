// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// glyphs is the character set for one rung of the capability ladder.
type glyphs struct {
	full, empty, wait, marker string
	tl, tr, bl, br, h, v      string
	teeL, teeR                string
	collapsed, expanded       string
	focus                     string
	ellipsis, dot             string
}

var unicodeGlyphs = glyphs{
	full: "█", empty: "░", wait: "─", marker: "█",
	tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│", teeL: "├", teeR: "┤",
	collapsed: "▸", expanded: "▾", focus: "›", ellipsis: "…", dot: "·",
}

var asciiGlyphs = glyphs{
	full: "#", empty: ".", wait: "-", marker: "#",
	tl: "+", tr: "+", bl: "+", br: "+", h: "-", v: "|", teeL: "+", teeR: "+",
	collapsed: ">", expanded: "v", focus: ">", ellipsis: "...", dot: "-",
}

// theme is how the screen draws at one capability level. Every state is a WORD; colour
// only repeats what the word says, so L1 (NO_COLOR) loses nothing.
//
//	L4 truecolor + Unicode: brand colours, moving marker for indeterminate work
//	L3 256 colours + Unicode: the nearest 256 colours, moving marker
//	L2 16 colours or uncertain Unicode: ASCII, words and borders only, static "working"
//	L1 NO_COLOR: monochrome, static "working"
type theme struct {
	g       glyphs
	animate bool
	ok      lipgloss.Style
	warn    lipgloss.Style
	fail    lipgloss.Style
	acc     lipgloss.Style
	dim     lipgloss.Style
	bold    lipgloss.Style
}

// Brand tokens (the review mockups' terminal palette).
const (
	colorOK     = "#5fd39a"
	colorWarn   = "#f5c451"
	colorFail   = "#f0727a"
	colorAccent = "#24a3d6"
	colorDim    = "#8aa6b6"
)

func newTheme(caps terminal.Capabilities) theme {
	// The colour profile is chosen here from the level, explicitly, and every colour
	// is converted to it before it reaches a style: nothing reads the environment and
	// nothing queries the terminal (Lip Gloss v2 never asks for the background, and
	// the palette assumes a dark one).
	t := theme{g: asciiGlyphs}
	plain := lipgloss.NewStyle()
	t.ok, t.warn, t.fail, t.acc, t.dim, t.bold = plain, plain, plain, plain, plain, plain
	switch caps.Level {
	case terminal.L4, terminal.L3:
		profile := colorprofile.TrueColor
		if caps.Level == terminal.L3 {
			profile = colorprofile.ANSI256
		}
		fg := func(hex string) lipgloss.Style {
			return lipgloss.NewStyle().Foreground(profile.Convert(lipgloss.Color(hex)))
		}
		t.animate = true
		if caps.Unicode {
			t.g = unicodeGlyphs
		}
		t.ok = fg(colorOK)
		t.warn = fg(colorWarn)
		t.fail = fg(colorFail)
		t.acc = fg(colorAccent)
		t.dim = fg(colorDim)
		t.bold = lipgloss.NewStyle().Bold(true)
	case terminal.L2:
		// Words and borders only. Bold is the one attribute every 16-colour
		// terminal draws.
		t.bold = lipgloss.NewStyle().Bold(true)
		t.fail = t.bold
	default:
		// L1 (and anything below it that reaches the screen): no attribute at all.
		if caps.Unicode {
			t.g = unicodeGlyphs
		}
	}
	return t
}

// paint styles s, and leaves an empty string empty: no escape pair around nothing.
func paint(st lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return st.Render(s)
}

// width is a string's display width in cells.
func width(s string) int { return lipgloss.Width(s) }

// truncate cuts plain text to w cells, marking the cut. It is for text that has not
// been styled yet; styling happens after.
func truncate(s string, w int, ellipsis string) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	ew := width(ellipsis)
	if ew >= w {
		ellipsis, ew = "", 0
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := width(string(r))
		if used+rw > w-ew {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + ellipsis
}

// pad right-pads styled text to w cells (measured by display width, never bytes).
func pad(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}
