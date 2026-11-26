// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package result

import (
	"errors"
	"strings"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Input is everything a result screen and the exit summary are drawn from.
//
// 🔴 EVERY FIELD IS ALLOWLISTED BY ITS TYPE. Text arrives as SafeText, choices as
// booleans and numbers, credentials as SecretRefs. Err is the one field that may
// hold anything, and it is never drawn raw: see Cause.
type Input struct {
	// Summary is the run's verdict, exactly as the hub delivered it.
	Summary installflow.Summary
	// Failure is the step.failed event that closed the failed step, when there was
	// one. A run that failed between stages, or before it started, has none.
	Failure *installflow.StepFailed
	// Target is the run.started target, or the zero value when the run never started.
	Target installflow.Target
	// Facts are the resolved choices the success screen reports.
	Facts Facts
	// Version is dcctl's version, for the header.
	Version string
	// Equivalent is the non-interactive command that reproduces this request with
	// its approval: the re-run command after a failure, the record after a success.
	Equivalent Command
	// Shell is how the commands this package composes itself are quoted.
	Shell Shell
	// Err is the engine's own error. It is returned to the command unchanged.
	Err error
	// Redact is the hub's redaction (Hub.Redact). Without it the error's text is
	// never drawn: the screen says where to read it instead.
	Redact func(string) string
	// Settled is closed once the run has settled (Admission.Settled): its engine
	// returned and no tool it stopped waiting for is still running. Nil means a
	// re-run is not offered from this screen at all (a progress-only run).
	Settled <-chan struct{}
	// BootstrapUI says dcctl bootstrap accepts --ui, so the next step may ask for it.
	BootstrapUI bool
}

// Facts are the settings of the install that the success screen reports. They
// are copied from the resolved choices, field by field: nothing here can carry a
// credential, a credential file's path or a URL.
type Facts struct {
	Provider       string // the provider argument, e.g. local
	Managed        bool   // a kind cluster dcctl manages, rather than an existing context
	Cluster        installflow.SafeText
	KubeContext    installflow.SafeText
	Compact        bool
	HA             bool
	TLS            bool
	Monitoring     bool
	Backups        bool
	MaxConnections int // 0: the install's default
}

// Command is a display-safe command line: credential values already replaced
// with a placeholder by the serialiser, which then marks it a Template.
type Command struct {
	Line installflow.SafeText
	// Args are Line's words exactly as they appear in it, quotes and all, in
	// order, as the serialiser quoted them. When they join with single spaces to
	// Line, the screen breaks the line only between them; otherwise it finds the
	// words by reading Line's quoting.
	Args     []installflow.SafeText
	Template bool
}

// words are the command's words, for wrapping.
func (c Command) words(shell Shell) []string {
	if len(c.Args) > 0 {
		ws := make([]string, len(c.Args))
		for i, a := range c.Args {
			ws[i] = a.String()
		}
		if strings.Join(ws, " ") == c.Line.String() {
			return ws
		}
	}
	return splitCommand(c.Line.String(), shell)
}

// Shell selects the quoting of a composed command.
type Shell uint8

const (
	POSIX Shell = iota
	PowerShell
)

// Cause is the failure's cause as it may be shown: the words for a stop the
// engine names, otherwise the engine's error after the hub's redaction and
// sanitising. Without a redactor the error is not shown at all.
func (in Input) Cause() string {
	if in.Failure != nil {
		if w := installflow.ErrorWords(in.Failure.Code); w != "" {
			return w
		}
	}
	if in.Summary.DeliveryIncomplete {
		return "The run's progress could not be fully reported, so it is not known to have finished."
	}
	if in.Err != nil && in.Redact != nil {
		if t, ok := redactedText(in.Redact, in.Err.Error()); ok && !t.IsZero() {
			return t.String()
		}
	}
	return "dcctl prints the install's error when you leave this screen."
}

// maxRedactPasses bounds the redaction loop. Each pass that changes the text
// replaces at least one registered value with the shorter-or-equal marker, so
// real input settles in two or three; one that has not settled by then is
// withheld rather than drawn.
const maxRedactPasses = 8

// redactedText is raw text made safe to draw, or false when it cannot be.
//
// 🔴 SANITISE, THEN REDACT, UNTIL NOTHING CHANGES. Sanitising removes escape
// sequences, control bytes and invisible format characters, and so can JOIN what
// they split: "secret\x1b[31mvalue" matches no registered secret, and becomes
// "secretvalue" once the escape is gone. And a replacement is text too: the
// marker that replaced one secret can complete another ("prefix" + the marker is
// a registered value). So the text is redacted as it came (a secret whose own
// form holds a line break), then sanitised and redacted again and again until a
// pass changes nothing: what is returned holds no registered value.
func redactedText(redact func(string) string, raw string) (installflow.SafeText, bool) {
	cur := installflow.Text(redact(raw))
	for range maxRedactPasses {
		next := installflow.Text(redact(cur.String()))
		if next == cur {
			return cur, true
		}
		cur = next
	}
	return installflow.SafeText{}, false
}

// withheld stands for a value that redaction could not settle.
const withheld = "[withheld]"

// shown is a value as it may be drawn or printed: the hub's redaction applied
// to a fixed point, as for the cause. Every value has been through the hub
// already; this re-checks it where it is composed into a line, since joining
// two safe values can make an unsafe one. Without a redactor it is unchanged.
func (in Input) shown(s string) string {
	if in.Redact == nil || s == "" {
		return s
	}
	t, ok := redactedText(in.Redact, s)
	if !ok {
		return withheld
	}
	return t.String()
}

// ErrIncomplete is returned when the engine returned no error yet the run did not
// succeed: its verdict is not success, or it never sent one.
var ErrIncomplete = errors.New("the run's progress could not be reported, so it is not known to have finished")

// Verdict is what the command returns once the screen has closed.
//
// 🔴 THE ENGINE'S ERROR IS RETURNED UNCHANGED, WHATEVER THE SCREEN SHOWED. The
// failure screen has drawn its cause, but the command still prints the error and
// exits 1 with it, exactly as the plain output does; closing a failure screen is
// never a way to 0. A run with no error and no successful verdict is ErrIncomplete.
func Verdict(sum installflow.Summary, finished bool, engineErr error) error {
	if engineErr != nil {
		return engineErr
	}
	if !finished || !sum.Succeeded() || sum.DeliveryIncomplete {
		return ErrIncomplete
	}
	return nil
}
