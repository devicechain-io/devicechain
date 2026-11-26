// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package tofujson reads the machine-readable stream `tofu apply -json` writes on
// stdout and turns it into two things: a count of the resource actions the apply
// planned and completed, and the human-readable lines the same messages carry.
//
// It is a pure reader. It runs no command, holds no lock and touches no file, so
// recorded streams exercise it completely; the runner in package bootstrap is
// what feeds it.
//
// 🔴 THE COUNT IS ONLY EVER WHAT OPENTOFU SAID. The denominator is the plan's own
// change summary, and the numerator is the number of create, update and delete
// actions OpenTofu reported complete. Nothing here completes a count on the
// apply's behalf: not its final summary, not its exit, not its failure. A failed
// apply that finished four of five actions stays at four of five.
package tofujson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// MaxLine bounds the buffer for an ordinary message. Longer lines are counted
// and drained to their newline. Diagnostics retain bounded text and CNPG retry
// evidence through a streaming scanner; other oversized messages are discarded.
const MaxLine = 256 << 10

// Progress is a snapshot of one apply's resource actions.
type Progress struct {
	// Done is the number of create, update and delete actions OpenTofu reported
	// complete. It only grows.
	Done int64
	// Planned is the number of actions the plan's summary announced: to add plus
	// to change plus to destroy. A replacement is one destroy and one create, and
	// OpenTofu completes it as those two actions, so it counts twice on both sides.
	// Meaningful only when Known.
	Planned int64
	// Known says the plan's summary has been read, so Planned is the denominator.
	// Until then the total is unknown, not zero.
	Known bool
}

// Observer is an io.Writer for the stdout of one `tofu apply -json`.
//
// Write never fails and never blocks on anything but its own writers, so the
// command's stdout keeps draining whatever the observer makes of it. It is not
// safe for concurrent use: terraform-exec writes stdout from one goroutine, and
// Close, Errors and Progress are for after the command has returned.
type Observer struct {
	human  io.Writer
	diags  io.Writer
	report func(Progress)

	pending  []byte
	skipping bool // inside a line longer than MaxLine, until its newline
	large    *oversizedDiagnostic

	progress Progress
	errs     []string

	// Malformed counts the lines that were not an OpenTofu message, and Oversized
	// the lines longer than MaxLine. Neither moves the count.
	Malformed int
	Oversized int
}

// New returns an observer that writes each message's text to human, each
// diagnostic to diags, and calls report with a snapshot whenever the count
// changes. report runs inside Write, so it must not block.
func New(human, diags io.Writer, report func(Progress)) *Observer {
	if human == nil {
		human = io.Discard
	}
	if diags == nil {
		diags = io.Discard
	}
	if report == nil {
		report = func(Progress) {}
	}
	return &Observer{human: human, diags: diags, report: report}
}

// Write reads complete lines from p and holds an incomplete last one. It always
// reports len(p), nil.
func (o *Observer) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			o.hold(p)
			return n, nil
		}
		o.hold(p[:i])
		p = p[i+1:]
		o.finishLine()
	}
	return n, nil
}

// hold appends part of a line, switching to skipping once it is over MaxLine.
func (o *Observer) hold(b []byte) {
	if o.skipping {
		o.large.write(b)
		return
	}
	if len(o.pending)+len(b) > MaxLine {
		o.Oversized++
		o.skipping = true
		o.large = &oversizedDiagnostic{}
		o.large.write(o.pending)
		o.large.write(b)
		o.pending = o.pending[:0]
		return
	}
	o.pending = append(o.pending, b...)
}

// Close reads a last line that had no newline. A stream cut off mid-message leaves
// a fragment that is not JSON, which is counted as malformed like any other.
func (o *Observer) Close() {
	if o.skipping || len(o.pending) > 0 {
		o.finishLine()
	}
	o.pending = nil
}

func (o *Observer) finishLine() {
	if o.skipping {
		if text, isError := o.large.result(); text != "" {
			_, _ = fmt.Fprintf(o.diags, "\n%s\n", text)
			if isError {
				o.errs = append(o.errs, text)
			}
		}
	} else {
		o.line(o.pending)
	}
	o.pending, o.skipping, o.large = o.pending[:0], false, nil
}

// Progress is the latest snapshot.
func (o *Observer) Progress() Progress { return o.progress }

// Errors is the text of every error diagnostic OpenTofu reported, in order, or "".
//
// 🔴 IN -json MODE THIS IS THE ONLY PLACE AN APPLY'S ERRORS ARE. OpenTofu writes its
// diagnostics into the JSON stream instead of to stderr, so the error terraform-exec
// returns carries an exit status and nothing else. Every caller that reads an apply
// error's text (the CloudNativePG admission retry is one) needs these appended.
func (o *Observer) Errors() string { return strings.Join(o.errs, "\n\n") }

// message is the part of an OpenTofu JSON UI message the observer reads.
type message struct {
	Level   string `json:"@level"`
	Message string `json:"@message"`
	Type    string `json:"type"`
	Changes *struct {
		Add       int64  `json:"add"`
		Change    int64  `json:"change"`
		Remove    int64  `json:"remove"`
		Operation string `json:"operation"`
	} `json:"changes"`
	Hook *struct {
		Action string `json:"action"`
	} `json:"hook"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
}

func (o *Observer) line(b []byte) {
	b = bytes.TrimRight(b, "\r")
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil || m.Type == "" {
		// Not a message. It is still OpenTofu's output, so the operator sees it;
		// it just counts for nothing.
		o.Malformed++
		_, _ = fmt.Fprintf(o.human, "%s\n", b)
		return
	}
	switch m.Type {
	case "diagnostic":
		o.diagnostic(m)
		return
	case "change_summary":
		// The plan's summary is the denominator, read once. The apply's own closing
		// summary is deliberately NOT used: it arrives only on success and says what
		// was done in total, and treating it as progress is exactly how a count would
		// reach its end on the apply's word rather than on the actions it reported.
		if m.Changes != nil && m.Changes.Operation == "plan" && !o.progress.Known {
			o.progress.Planned = m.Changes.Add + m.Changes.Change + m.Changes.Remove
			o.progress.Known = true
			o.report(o.progress)
		}
	case "apply_complete":
		if m.Hook != nil && countedAction(m.Hook.Action) {
			o.progress.Done++
			o.report(o.progress)
		}
	}
	if m.Message != "" {
		_, _ = fmt.Fprintf(o.human, "%s\n", m.Message)
	}
}

// countedAction is an action the plan's summary counts. A read (a data source
// read during the apply) and a no-op are in no summary.
func countedAction(a string) bool { return a == "create" || a == "update" || a == "delete" }

func (o *Observer) diagnostic(m message) {
	d := m.Diagnostic
	if d == nil {
		if m.Message != "" {
			_, _ = fmt.Fprintf(o.diags, "%s\n", m.Message)
		}
		return
	}
	label := "Warning"
	if d.Severity == "error" {
		label = "Error"
	}
	text := label + ": " + d.Summary
	if d.Detail != "" {
		text += "\n\n" + d.Detail
	}
	_, _ = fmt.Fprintf(o.diags, "\n%s\n", text)
	if d.Severity == "error" {
		o.errs = append(o.errs, text)
	}
}
