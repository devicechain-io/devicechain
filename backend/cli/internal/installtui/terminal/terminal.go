// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package terminal defines the installer's terminal policy without performing
// terminal I/O. The entry adapter supplies observations before starting Tea;
// Tea alone owns console mode changes and restoration.
package terminal

import (
	"strings"

	"github.com/charmbracelet/colorprofile"
)

// UI is the requested presenter. The zero value deliberately preserves plain.
type UI string

const (
	Plain UI = "plain"
	TUI   UI = "tui"
	Auto  UI = "auto"
)

// Level is the capability ladder, from today's plain output through truecolor.
type Level uint8

const (
	L0 Level = iota
	L1
	L2
	L3
	L4
)

// Layout describes available space, independently of art eligibility.
type Layout uint8

const (
	UnknownLayout Layout = iota
	Minimal
	Compact
	SingleColumn
	Full
)

// Art is the initial splash policy. NoArt also suppresses the splash pause.
type Art uint8

const (
	NoArt Art = iota
	TextHeader
	CompactMark
	LargeMark
)

// Inputs are observations and parsed options, never live handles. Env is read
// only. Probe all three streams with x/term before any terminal side effect;
// obtain size with GetSize and profile with colorprofile on that output handle.
// WindowsVT and WindowsUnicode must come from console capability probes, not
// WT_SESSION, TERM, locale, or the name of the terminal application. A failed
// probe is false. This package never opens /dev/tty or CONIN$ or enables VT.
type Inputs struct {
	InputTTY, OutputTTY, ErrorTTY bool
	GOOS                          string
	Env                           map[string]string
	Width, Height                 int
	SizeKnown                     bool
	Profile                       colorprofile.Profile
	WindowsVT, WindowsUnicode     bool
	UI                            UI
	Yes, Dev, DryRun, NoSplash    bool
	Informational                 bool // help, version, or a parse/validation error
}

// Capabilities is a value snapshot. No environment, handles, or mutable data are
// retained. Eligible describes the terminal; TUI also applies invocation policy.
// Pass ColorDepth explicitly to Lip Gloss instead of allowing it to reprobe.
type Capabilities struct {
	InputTTY, OutputTTY, ErrorTTY bool
	CI                            bool
	Width, Height                 int
	SizeKnown                     bool
	VT, Unicode                   bool
	ColorDepth                    colorprofile.Profile
	Remote, NoSplash, NoColor     bool
	Eligible, TUI, ProgressOnly   bool
	Level                         Level
	Layout                        Layout
	Splash                        Art
	Animate                       bool
}

// Evaluate implements spec sections 7-9 as a pure function. In particular it
// cannot change modes, write escapes, pause, or compete with the prompt broker.
func Evaluate(in Inputs) Capabilities {
	c := Capabilities{
		InputTTY: in.InputTTY, OutputTTY: in.OutputTTY, ErrorTTY: in.ErrorTTY,
		CI: in.Env["CI"] != "", Width: in.Width, Height: in.Height,
		SizeKnown:  in.SizeKnown && in.Width > 0 && in.Height > 0,
		ColorDepth: in.Profile, NoSplash: in.NoSplash,
	}
	_, c.NoColor = in.Env["NO_COLOR"] // presence wins, including an empty value
	for name, value := range in.Env {
		if strings.HasPrefix(name, "SSH_") && value != "" {
			c.Remote = true
		}
	}
	term := in.Env["TERM"]
	if in.GOOS == "windows" {
		c.VT, c.Unicode = in.WindowsVT, in.WindowsUnicode
	} else {
		c.VT = term != "" && term != "dumb"
		c.Unicode = utf8Locale(in.Env)
	}
	if c.NoColor || (c.ColorDepth != colorprofile.TrueColor && c.ColorDepth != colorprofile.ANSI256 && c.ColorDepth != colorprofile.ANSI) {
		c.ColorDepth = colorprofile.ASCII
	}
	c.Eligible = c.InputTTY && c.OutputTTY && c.ErrorTTY && !c.CI && c.VT && term != "dumb"
	if !c.Eligible {
		return c
	}
	switch {
	case c.ColorDepth == colorprofile.ASCII:
		c.Level = L1
	case !c.Unicode || c.ColorDepth == colorprofile.ANSI:
		c.Level = L2
	case c.ColorDepth == colorprofile.ANSI256:
		c.Level = L3
	case c.ColorDepth == colorprofile.TrueColor:
		c.Level = L4
	}
	if c.SizeKnown {
		switch {
		case c.Width < 60 || c.Height < 16:
			c.Layout = Minimal
		case c.Width >= 100 && c.Height >= 30:
			c.Layout = Full
		case c.Width >= 80:
			c.Layout = SingleColumn
		default:
			c.Layout = Compact
		}
	}
	c.TUI = (in.UI == TUI || in.UI == Auto) && !in.DryRun && !in.Informational
	c.ProgressOnly = c.TUI && (in.Yes || in.Dev)
	if !c.TUI || c.NoSplash || in.Yes || in.Dev || !c.SizeKnown {
		return c
	}
	c.Splash = TextHeader
	if c.Level >= L3 && c.Width >= 80 && c.Height >= 20 {
		c.Splash = CompactMark
		if c.Height >= 24 {
			c.Splash = LargeMark
		}
	}
	c.Animate = c.Splash == LargeMark && !c.Remote && !c.NoColor
	return c
}

func utf8Locale(env map[string]string) bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if locale := env[name]; locale != "" {
			// POSIX codesets are case insensitive; an @modifier is not part
			// of the encoding. A non-UTF override must not fall through.
			locale = strings.ToUpper(strings.SplitN(locale, "@", 2)[0])
			codeset := strings.SplitN(locale, ".", 2)
			return codeset[len(codeset)-1] == "UTF-8" || codeset[len(codeset)-1] == "UTF8"
		}
	}
	return false
}
