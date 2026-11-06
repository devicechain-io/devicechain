// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import "testing"

const simStatusBody = `{"state":"running","devices":3,"emitted":{"events":12}}`

// A script that captures or pipes `sim status` must get exactly the line the sim sent.
func TestSimControlBodyIsUntouchedWhenNotATerminal(t *testing.T) {
	if got := renderControlBody(simStatusBody, false); got != simStatusBody {
		t.Fatalf("piped output changed:\n got %q\nwant %q", got, simStatusBody)
	}
}

// On a terminal the one-line object is indented, and indenting loses nothing.
func TestSimControlBodyIsIndentedOnATerminal(t *testing.T) {
	want := "{\n  \"state\": \"running\",\n  \"devices\": 3,\n  \"emitted\": {\n    \"events\": 12\n  }\n}"
	if got := renderControlBody(simStatusBody, true); got != want {
		t.Fatalf("terminal output:\n got %q\nwant %q", got, want)
	}
}

// Anything that is not JSON is shown as received, terminal or not.
func TestSimControlBodyThatIsNotJSONIsUntouched(t *testing.T) {
	for _, tty := range []bool{true, false} {
		if got := renderControlBody("sim is warming up", tty); got != "sim is warming up" {
			t.Fatalf("tty=%v: %q", tty, got)
		}
	}
}
