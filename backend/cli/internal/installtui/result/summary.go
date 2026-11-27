// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"fmt"
	"io"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// WriteExitSummary writes the short, durable record left in the scrollback once
// the terminal is restored: the outcome, the target, where it stopped, what was
// changed, and the commands to run next. It is plain text with no escape
// sequence, for a terminal that has just left the program.
//
// 🔴 IT IS NEVER A PLACE FOR A SECRET, AND NEVER FOR THE ENGINE'S ERROR. The error
// is the command's to print, on stderr, after the hub's redaction; this summary is
// built only from allowlisted values: codes and their words, safe texts, booleans
// and the display-safe commands. A one-time value is never printed here again.
func WriteExitSummary(w io.Writer, in Input) error {
	var b strings.Builder
	// Every line is re-checked as it is composed, label and padding included:
	// joining safe values can complete a registered one.
	put := func(s, fallback string) {
		b.WriteString(in.line(s, fallback))
		b.WriteByte('\n')
	}
	line := func(label, value string) {
		if value == "" {
			return
		}
		put(fmt.Sprintf("  %-9s %s", label+":", in.shown(value)), fmt.Sprintf("  %-9s %s", label+":", withheld))
	}
	put("dcctl install: "+in.outcome(), "dcctl install: "+withheld)
	line("Target", in.target())
	switch kindOf(in.Summary) {
	case kindSucceeded, kindPlanned:
		for _, c := range in.Summary.Credentials {
			line("Secret", credentialWords(c))
		}
		for _, n := range in.nextSteps() {
			line("Next", n)
		}
		line("Command", in.Equivalent.display(in.Shell))
	default:
		line("Stage", in.failedAt())
		line("Cause", stopWords(in))
		line("Changes", in.changes())
		line("Claim", claimWords(in.Summary.Claim))
		line("Inspect", in.inspect())
		line("Re-run", in.Equivalent.display(in.Shell))
	}
	if in.Equivalent.Template && in.Equivalent.Line != "" {
		put("  (template: supply the redacted credentials privately)", "")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// stopWords is the cause the exit summary may repeat: only a stop the engine
// names in its own words. A stage's own error is the command's to print, so it
// has no line here.
func stopWords(in Input) string {
	switch {
	case in.recordedButNotReady():
		return "Install recorded; replicas still joining."
	case in.Summary.DeliveryIncomplete:
		return "The run's progress could not be fully reported."
	case in.Failure != nil:
		return installflow.ErrorWords(in.Failure.Code)
	}
	return ""
}
