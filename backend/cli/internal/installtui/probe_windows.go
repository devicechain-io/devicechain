// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// consoleSupportsVT reports whether out is a console that accepts virtual-terminal
// processing. It detects behaviour, not branding: WT_SESSION and TERM say nothing about
// whether this handle is a console. Legacy conhost refuses the mode, and the run stays
// plain; see probeVT for what happens when the mode cannot be put back.
func consoleSupportsVT(out *os.File) bool {
	h := windows.Handle(out.Fd())
	ok, restore := probeVT(windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING,
		func() (uint32, error) {
			var mode uint32
			err := windows.GetConsoleMode(h, &mode)
			return mode, err
		},
		func(mode uint32) error { return windows.SetConsoleMode(h, mode) })
	keepProbeRestore(restore)
	return ok
}

// consoleReadsPastGate reports whether Bubble Tea will read in from the console handle
// itself rather than through the stdin gate: stdin is a console.
func consoleReadsPastGate(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(f.Fd()), &mode) == nil
}
