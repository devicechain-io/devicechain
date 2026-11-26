// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package terminal

import (
	"reflect"
	"testing"

	"github.com/muesli/termenv"
)

func TestDefaultRemainsPlain(t *testing.T) {
	in := baseline()
	in.UI = ""
	c := Evaluate(in)
	if c.TUI || c.Splash != NoArt || c.Animate {
		t.Fatalf("default must preserve plain output: %+v", c)
	}
}

func baseline() Inputs {
	return Inputs{
		InputTTY: true, OutputTTY: true, ErrorTTY: true, GOOS: "linux",
		Env:   map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"},
		Width: 120, Height: 40, SizeKnown: true, Profile: termenv.TrueColor, UI: TUI,
	}
}

func TestEligibilityTable(t *testing.T) {
	type decision struct {
		eligible, tui, progress bool
		level                   Level
		layout                  Layout
		art                     Art
		animate                 bool
	}
	full := decision{true, true, false, L4, Full, LargeMark, true}
	plain := decision{}
	row := func(change func(*Inputs), want decision) struct {
		change func(*Inputs)
		want   decision
	} {
		return struct {
			change func(*Inputs)
			want   decision
		}{change, want}
	}
	// Each row changes one observation from a known eligible local session.
	cases := map[string]struct {
		change func(*Inputs)
		want   decision
	}{
		"local truecolor":              row(func(*Inputs) {}, full),
		"stdin pipe":                   row(func(i *Inputs) { i.InputTTY = false }, plain),
		"stdout pipe":                  row(func(i *Inputs) { i.OutputTTY = false }, plain),
		"stderr pipe":                  row(func(i *Inputs) { i.ErrorTTY = false }, plain),
		"CI nonempty even false":       row(func(i *Inputs) { i.Env["CI"] = "false" }, plain),
		"CI empty":                     row(func(i *Inputs) { i.Env["CI"] = "" }, full),
		"TERM dumb":                    row(func(i *Inputs) { i.Env["TERM"] = "dumb" }, plain),
		"TERM absent Unix":             row(func(i *Inputs) { delete(i.Env, "TERM") }, plain),
		"explicit plain":               row(func(i *Inputs) { i.UI = Plain }, decision{true, false, false, L4, Full, NoArt, false}),
		"auto":                         row(func(i *Inputs) { i.UI = Auto }, full),
		"unknown mode fails closed":    row(func(i *Inputs) { i.UI = "bogus" }, decision{true, false, false, L4, Full, NoArt, false}),
		"yes progress only":            row(func(i *Inputs) { i.Yes = true }, decision{true, true, true, L4, Full, NoArt, false}),
		"dev progress only":            row(func(i *Inputs) { i.Dev = true }, decision{true, true, true, L4, Full, NoArt, false}),
		"dry run":                      row(func(i *Inputs) { i.DryRun = true }, decision{true, false, false, L4, Full, NoArt, false}),
		"help version parse error":     row(func(i *Inputs) { i.Informational = true }, decision{true, false, false, L4, Full, NoArt, false}),
		"no splash":                    row(func(i *Inputs) { i.NoSplash = true }, decision{true, true, false, L4, Full, NoArt, false}),
		"L3":                           row(func(i *Inputs) { i.Profile = termenv.ANSI256 }, decision{true, true, false, L3, Full, LargeMark, true}),
		"L2 ANSI16":                    row(func(i *Inputs) { i.Profile = termenv.ANSI }, decision{true, true, false, L2, Full, TextHeader, false}),
		"L2 uncertain Unicode":         row(func(i *Inputs) { i.Env["LANG"] = "C" }, decision{true, true, false, L2, Full, TextHeader, false}),
		"L1 NO_COLOR beats force":      row(func(i *Inputs) { i.Env["NO_COLOR"] = "1"; i.Env["CLICOLOR_FORCE"] = "1" }, decision{true, true, false, L1, Full, TextHeader, false}),
		"L1 NO_COLOR empty present":    row(func(i *Inputs) { i.Env["NO_COLOR"] = "" }, decision{true, true, false, L1, Full, TextHeader, false}),
		"L1 no color profile":          row(func(i *Inputs) { i.Profile = termenv.Ascii }, decision{true, true, false, L1, Full, TextHeader, false}),
		"unknown profile fails closed": row(func(i *Inputs) { i.Profile = termenv.Profile(100) }, decision{true, true, false, L1, Full, TextHeader, false}),
		"SSH connection static":        row(func(i *Inputs) { i.Env["SSH_CONNECTION"] = "remote" }, decision{true, true, false, L4, Full, LargeMark, false}),
		"SSH client static":            row(func(i *Inputs) { i.Env["SSH_CLIENT"] = "remote" }, decision{true, true, false, L4, Full, LargeMark, false}),
		"SSH tty static":               row(func(i *Inputs) { i.Env["SSH_TTY"] = "/dev/pts/1" }, decision{true, true, false, L4, Full, LargeMark, false}),
		"SSH empty local":              row(func(i *Inputs) { i.Env["SSH_TTY"] = "" }, full),
		"unknown size":                 row(func(i *Inputs) { i.SizeKnown = false }, decision{true, true, false, L4, UnknownLayout, NoArt, false}),
		"zero width":                   row(func(i *Inputs) { i.Width = 0 }, decision{true, true, false, L4, UnknownLayout, NoArt, false}),
		"zero height":                  row(func(i *Inputs) { i.Height = 0 }, decision{true, true, false, L4, UnknownLayout, NoArt, false}),
		"negative size":                row(func(i *Inputs) { i.Width = -1 }, decision{true, true, false, L4, UnknownLayout, NoArt, false}),
		"100x30 full boundary":         row(func(i *Inputs) { i.Width = 100; i.Height = 30 }, full),
		"100x29 single":                row(func(i *Inputs) { i.Width = 100; i.Height = 29 }, decision{true, true, false, L4, SingleColumn, LargeMark, true}),
		"99x30 single":                 row(func(i *Inputs) { i.Width = 99; i.Height = 30 }, decision{true, true, false, L4, SingleColumn, LargeMark, true}),
		"80x24 animation boundary":     row(func(i *Inputs) { i.Width = 80; i.Height = 24 }, decision{true, true, false, L4, SingleColumn, LargeMark, true}),
		"80x23 static compact mark":    row(func(i *Inputs) { i.Width = 80; i.Height = 23 }, decision{true, true, false, L4, SingleColumn, CompactMark, false}),
		"80x20 compact mark":           row(func(i *Inputs) { i.Width = 80; i.Height = 20 }, decision{true, true, false, L4, SingleColumn, CompactMark, false}),
		"80x19 text":                   row(func(i *Inputs) { i.Width = 80; i.Height = 19 }, decision{true, true, false, L4, SingleColumn, TextHeader, false}),
		"79x24 no logo":                row(func(i *Inputs) { i.Width = 79; i.Height = 24 }, decision{true, true, false, L4, Compact, TextHeader, false}),
		"60x20 compact":                row(func(i *Inputs) { i.Width = 60; i.Height = 20 }, decision{true, true, false, L4, Compact, TextHeader, false}),
		"60x16 compact boundary":       row(func(i *Inputs) { i.Width = 60; i.Height = 16 }, decision{true, true, false, L4, Compact, TextHeader, false}),
		"59x20 minimal":                row(func(i *Inputs) { i.Width = 59; i.Height = 20 }, decision{true, true, false, L4, Minimal, TextHeader, false}),
		"80x15 minimal":                row(func(i *Inputs) { i.Width = 80; i.Height = 15 }, decision{true, true, false, L4, Minimal, TextHeader, false}),
		"Windows ConPTY no TERM": row(func(i *Inputs) {
			i.GOOS = "windows"
			delete(i.Env, "TERM")
			i.WindowsVT = true
			i.WindowsUnicode = true
		}, full),
		"Windows legacy conhost": row(func(i *Inputs) { i.GOOS = "windows"; i.WindowsVT = false; i.Env["WT_SESSION"] = "not-proof" }, plain),
		"Windows pipe despite WT_SESSION": row(func(i *Inputs) {
			i.GOOS = "windows"
			i.WindowsVT = true
			i.OutputTTY = false
			i.Env["WT_SESSION"] = "not-proof"
		}, plain),
		"Windows uncertain Unicode": row(func(i *Inputs) { i.GOOS = "windows"; i.WindowsVT = true }, decision{true, true, false, L2, Full, TextHeader, false}),
		"Windows dumb":              row(func(i *Inputs) { i.GOOS = "windows"; i.WindowsVT = true; i.Env["TERM"] = "dumb" }, plain),
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := baseline()
			tc.change(&in)
			c := Evaluate(in)
			got := decision{c.Eligible, c.TUI, c.ProgressOnly, c.Level, c.Layout, c.Splash, c.Animate}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v; capabilities %+v", got, tc.want, c)
			}
			if c.NoColor && c.ColorDepth != termenv.Ascii {
				t.Fatal("NO_COLOR did not override output profile")
			}
			if c.InputTTY != in.InputTTY || c.OutputTTY != in.OutputTTY || c.ErrorTTY != in.ErrorTTY || c.Width != in.Width || c.Height != in.Height || c.NoSplash != in.NoSplash {
				t.Fatal("capability snapshot lost observations")
			}
		})
	}
}

func TestLocalePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, all, ctype, lang string
		unicode                bool
	}{
		{"LANG", "", "", "en_US.UTF-8", true},
		{"LC_CTYPE wins", "", "C", "en_US.UTF-8", false},
		{"LC_ALL wins", "C", "en_US.UTF-8", "en_US.UTF-8", false},
		{"LC_ALL UTF8", "C.utf8", "C", "C", true},
		{"modifier", "en_US.utf-8@custom", "", "", true},
		{"unset", "", "", "", false},
		{"other encoding", "en_US.ISO-8859-1", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := baseline()
			in.Env = map[string]string{"TERM": "xterm", "LC_ALL": tc.all, "LC_CTYPE": tc.ctype, "LANG": tc.lang}
			if got := Evaluate(in).Unicode; got != tc.unicode {
				t.Fatalf("Unicode = %v, want %v", got, tc.unicode)
			}
		})
	}
}

func TestEvaluationUsesOnlyInjectedInputs(t *testing.T) {
	in := baseline()
	before := map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}
	want := Evaluate(in)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CI", "1")
	t.Setenv("TERM", "dumb")
	t.Setenv("SSH_CONNECTION", "remote")
	if got := Evaluate(in); got != want || !reflect.DeepEqual(in.Env, before) {
		t.Fatalf("evaluation used ambient state or changed inputs: %+v", got)
	}
}
