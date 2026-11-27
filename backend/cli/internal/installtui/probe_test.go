// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"os"
	"testing"

	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// A redirected stream is never a terminal, whichever of the three it is, and a session
// with one is never handed to the terminal UI.
func TestProbeOfRedirectedStreams(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	in := Probe(r, w, w)
	if in.InputTTY || in.OutputTTY || in.ErrorTTY || in.SizeKnown {
		t.Fatalf("pipes probed as a terminal: %+v", in)
	}
	in.UI = terminal.TUI
	if terminal.Evaluate(in).TUI {
		t.Fatal("a redirected session was given the terminal UI")
	}
	for _, mixed := range [][3]*os.File{{r, nil, nil}, {nil, w, nil}, {nil, nil, w}} {
		in := Probe(mixed[0], mixed[1], mixed[2])
		if in.InputTTY || in.OutputTTY || in.ErrorTTY {
			t.Fatalf("%v probed as a terminal: %+v", mixed, in)
		}
	}
}
