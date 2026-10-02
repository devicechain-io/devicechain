// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"strings"
	"testing"
)

// filtered runs text through a fresh outputsBlockFilter in chunks of size n (the whole
// text when n <= 0), then flushes, checking every Write reports the whole chunk taken.
func filtered(t *testing.T, text string, n int) string {
	t.Helper()
	var buf bytes.Buffer
	f := newOutputsBlockFilter(&buf)
	p := []byte(text)
	if n <= 0 {
		n = len(p) + 1
	}
	for len(p) > 0 {
		c := p[:min(n, len(p))]
		p = p[len(c):]
		got, err := f.Write(c)
		if err != nil || got != len(c) {
			t.Fatalf("Write(%q) = %d, %v; want %d, nil: dropping a line is not a short write", c, got, err, len(c))
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return buf.String()
}

// TestTheOutputsFilterIsIndifferentToHowTheStreamIsCut pins that the result depends on
// the lines, not on where the writes fell. terraform-exec writes whole lines, but the
// filter must not rely on it.
func TestTheOutputsFilterIsIndifferentToHowTheStreamIsCut(t *testing.T) {
	want := strings.Replace(destroyTranscript, destroyTranscriptBlock, destroyOutputsNotShown+"\n", 1)
	for _, n := range []int{0, 1, 7} {
		if got := filtered(t, destroyTranscript, n); got != want {
			t.Errorf("in writes of %d bytes (0 = one write): got\n%s\nwant\n%s", n, got, want)
		}
	}
}

// TestTheOutputsFilterDropsOnlyTheBlock pins where the block starts and ends.
func TestTheOutputsFilterDropsOnlyTheBlock(t *testing.T) {
	r := destroyOutputsNotShown + "\n"
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "no header passes through byte for byte, an unterminated last line included",
			in:   "Plan: 0 to add, 0 to change, 1 to destroy.\n\n  - resource \"x\" {\n    }\nDestroy complete!",
			want: "Plan: 0 to add, 0 to change, 1 to destroy.\n\n  - resource \"x\" {\n    }\nDestroy complete!",
		},
		{
			// The indented line after the blank one proves the blank line ENDED the block:
			// a filter that stayed in it would drop that line.
			name: "a blank line ends the block and is printed, and indented lines after it are kept",
			in:   "Changes to Outputs:\n  - a = 1 -> null\n\n  # terraform_data.x will be destroyed\nDone\n",
			want: r + "\n  # terraform_data.x will be destroyed\nDone\n",
		},
		{
			name: "a column-0 line ends the block, and indented lines after it are kept",
			in:   "Changes to Outputs:\n  - a = 1 -> null\nterraform_data.x: Destroying...\n  indented after the block\n",
			want: r + "terraform_data.x: Destroying...\n  indented after the block\n",
		},
		{
			name: "a multi-line value is dropped whole",
			in: "Changes to Outputs:\n  - nats_ca = <<-EOT\n            -----BEGIN CERTIFICATE-----\n" +
				"            MIIB\n        EOT -> null\n  - b = 2 -> null\nDestroy complete!\n",
			want: r + "Destroy complete!\n",
		},
		{
			name: "the header text indented inside a resource attribute is not the header",
			in:   "      - input  = \"Changes to Outputs:\" -> null\n  - kept = 1 -> null\nnext\n",
			want: "      - input  = \"Changes to Outputs:\" -> null\n  - kept = 1 -> null\nnext\n",
		},
		{
			// The match is exact and at column 0: an indented or differently capitalised
			// copy of the header, alone on its line, is not the header, so it and the
			// indented line after it are kept.
			name: "the header text alone but indented, or in other capitals, is not the header",
			in:   "  Changes to Outputs:\n  - kept = 1 -> null\nchanges to outputs:\n  - also kept = 2 -> null\n",
			want: "  Changes to Outputs:\n  - kept = 1 -> null\nchanges to outputs:\n  - also kept = 2 -> null\n",
		},
		{
			name: "a header ending in CRLF is the header",
			in:   "Changes to Outputs:\r\n  - a = 1 -> null\r\nnext\r\n",
			want: r + "next\r\n",
		},
		{
			name: "the block as the last thing in the stream, with no newline on its last line",
			in:   "Plan: 0 to add\nChanges to Outputs:\n  - a = 1 -> null",
			want: "Plan: 0 to add\n" + r,
		},
		{
			name: "an empty block",
			in:   "Changes to Outputs:\nnext\n",
			want: r + "next\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := filtered(t, tc.in, 0); got != tc.want {
				t.Errorf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}
