// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// CommandLines lays a command out for a screen outside this package, the review
// screen before a run: each word as the serialiser quoted it, a word the screen
// cannot draw exactly replaced by a placeholder that names the problem, the whole
// re-checked against redaction as one line.
//
// 🔴 WHAT IS DRAWN IS WHAT RUNS WHEN IT IS COPIED. Lines break only between words,
// and every line but the last ends in the shell's line continuation (" \" for POSIX,
// " `" for PowerShell), so pasting the lines runs the one command. A word is never
// split: when one does not fit the width even on a line of its own, ok is false and
// nothing is returned, and the caller says where the exact command can be read
// instead. Without a redactor the redaction pass is skipped, as it is for the
// result screens.
func CommandLines(c Command, shell Shell, redact func(string) string, width int) (lines []string, ok bool) {
	in := Input{Shell: shell, Redact: redact}
	words := c.words(shell)
	joined := strings.Join(words, " ")
	// 🔴 NEVER CUT. The words were each checked as drawable already; the whole is
	// checked against redaction UNCAPPED (as a composed line is, see unredacted),
	// since sanitising's length cap would cut a long command inside an argument and
	// drop what follows it.
	if redact != nil && !in.unredacted(joined) {
		s, settled := joined, false
		for range maxRedactPasses {
			next := redact(s)
			if next == s {
				settled = true
				break
			}
			s = next
		}
		if !settled {
			return nil, false
		}
		words = splitCommand(s, shell)
	}
	cont := " \\"
	if shell == PowerShell {
		cont = " `"
	}
	room := width - lipgloss.Width(cont)
	cur := ""
	for _, w := range words {
		if lipgloss.Width(w) > room {
			return nil, false
		}
		switch {
		case cur == "":
			cur = w
		case lipgloss.Width(cur+" "+w) <= room:
			cur += " " + w
		default:
			lines = append(lines, cur+cont)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines, true
}
