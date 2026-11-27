// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"
	"runtime"
	"strings"

	"github.com/charmbracelet/colorprofile"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
	"golang.org/x/term"
)

// Probe observes the process's three standard streams for terminal.Evaluate.
//
// 🔴 IT DECIDES TTY ELIGIBILITY BEFORE IT ASKS ANYTHING ELSE. Size, colour profile and the
// Windows console capabilities are read only when stdin, stdout and stderr are all
// terminals: a redirected run never has anything probed beyond isatty, so the plain path
// it falls back to is exactly the one it would have taken without --ui. Nothing here
// opens /dev/tty or CONIN$, writes an escape sequence, or queries the terminal (the
// colour profile comes from the environment and terminfo, never from an OSC round trip;
// under tmux, colorprofile asks `tmux info`, not the terminal).
//
// The caller fills the invocation half of Inputs (UI, Yes, Dev, DryRun, NoSplash).
func Probe(stdin, stdout, stderr *os.File) terminal.Inputs {
	in := terminal.Inputs{
		InputTTY:  isTerminal(stdin),
		OutputTTY: isTerminal(stdout),
		ErrorTTY:  isTerminal(stderr),
		GOOS:      runtime.GOOS,
		Env:       environ(),
	}
	if !in.InputTTY || !in.OutputTTY || !in.ErrorTTY {
		return in
	}
	if w, h, err := term.GetSize(int(stdout.Fd())); err == nil {
		in.Width, in.Height, in.SizeKnown = w, h, w > 0 && h > 0
	}
	in.Profile = colorprofile.Detect(stdout, os.Environ())
	if runtime.GOOS == "windows" {
		in.WindowsVT = consoleSupportsVT(stdout)
		// A console that renders VT sequences is the modern console host, and Go writes
		// to it with WriteConsoleW, so the text arrives as UTF-16 whatever the code page.
		in.WindowsUnicode = in.WindowsVT
	}
	return in
}

func isTerminal(f *os.File) bool { return f != nil && term.IsTerminal(int(f.Fd())) }

func environ() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}
