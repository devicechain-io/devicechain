// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// Brand tokens: the review mockups' terminal palette, the same the progress
// screen uses.
const (
	colorOK     = "#5fd39a"
	colorWarn   = "#f5c451"
	colorFail   = "#f0727a"
	colorAccent = "#24a3d6"
	colorDim    = "#8aa6b6"
)

type glyphs struct {
	tl, tr, bl, br, h, v, teeL, teeR string
	focus, dot                       string
}

var unicodeGlyphs = glyphs{tl: "╭", tr: "╮", bl: "╰", br: "╯", h: "─", v: "│", teeL: "├", teeR: "┤",
	focus: "›", dot: "·"}

var asciiGlyphs = glyphs{tl: "+", tr: "+", bl: "+", br: "+", h: "-", v: "|", teeL: "+", teeR: "+",
	focus: ">", dot: "-"}

// theme is how a result screen draws at one capability level. Every state is a
// WORD and focus is a glyph; colour and inverse only repeat them, so L1
// (NO_COLOR) loses nothing.
type theme struct {
	g                            glyphs
	ok, warn, fail, acc, dim, bd lipgloss.Style
	focus                        lipgloss.Style
}

func newTheme(caps terminal.Capabilities) theme {
	// The profile is chosen from the level here, and every colour converted to it
	// before it reaches a style: nothing reads the environment or asks the terminal.
	plain := lipgloss.NewStyle()
	t := theme{g: asciiGlyphs, ok: plain, warn: plain, fail: plain, acc: plain, dim: plain, bd: plain, focus: plain}
	switch caps.Level {
	case terminal.L4, terminal.L3:
		profile := colorprofile.TrueColor
		if caps.Level == terminal.L3 {
			profile = colorprofile.ANSI256
		}
		fg := func(hex string) lipgloss.Style {
			return lipgloss.NewStyle().Foreground(profile.Convert(lipgloss.Color(hex)))
		}
		if caps.Unicode {
			t.g = unicodeGlyphs
		}
		t.ok, t.warn, t.fail, t.acc, t.dim = fg(colorOK), fg(colorWarn), fg(colorFail), fg(colorAccent), fg(colorDim)
		t.bd = lipgloss.NewStyle().Bold(true)
		t.focus = lipgloss.NewStyle().Reverse(true)
	case terminal.L2:
		// Words and borders only; bold and inverse are attributes every 16-colour
		// terminal draws.
		t.bd = lipgloss.NewStyle().Bold(true)
		t.fail = t.bd
		t.focus = lipgloss.NewStyle().Reverse(true)
	default:
		// L1: no attribute at all.
		if caps.Unicode {
			t.g = unicodeGlyphs
		}
	}
	return t
}

// paint styles s, leaving an empty string empty.
func paint(st lipgloss.Style, s string) string {
	if s == "" {
		return ""
	}
	return st.Render(s)
}
