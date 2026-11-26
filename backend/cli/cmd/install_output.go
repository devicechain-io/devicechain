// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installjson"
	"github.com/spf13/pflag"
)

// installOutput is --output: text (what dcctl has always printed) or json (the
// event stream, one JSON object per line).
var installOutput string

const (
	installOutputText = "text"
	installOutputJSON = "json"

	installOutputUsage = "output format: text (the output dcctl has always printed) or json (the install's " +
		"events, one JSON object per line on stdout and nothing else there, for automation; it never " +
		"reads stdin, so a confirmation is given with --yes or the run is refused before it changes anything)"
)

// Test seams: the goldens fix the run id and the clock so a stream is byte for
// byte reproducible. Production leaves both unset (random id, system clock).
var (
	installStreamRunID string
	installStreamClock installflow.Clock
)

// installStream is one --output json invocation: the stream's presenter, the
// stdout it owns, and whether the run itself started.
type installStream struct {
	presenter installjson.Presenter
	stdout    *os.File
	ran       bool
}

// activeInstallStream is the --output json invocation in progress, or nil for
// text output. One command runs per process.
var activeInstallStream *installStream

// beginInstallOutput validates --output and, for json, takes stdout for the
// stream. It returns nil for text output, which changes nothing.
//
// 🔴 STDOUT BELONGS TO THE STREAM FROM HERE ON. os.Stdout is pointed at stderr
// for the rest of the command, and the stream alone holds the real stdout. The
// engine's own output is routed into the stream as log lines by the presenter;
// this is what keeps anything else — a notice the command prints before the run,
// a helper that still reaches for os.Stdout — from landing between two events.
// It is the line between a consumer that parses every line and one that breaks
// on the first stray byte.
func beginInstallOutput(fs *pflag.FlagSet) (*installStream, error) {
	switch installOutput {
	case installOutputText:
		return nil, nil
	case installOutputJSON:
	default:
		return nil, fmt.Errorf("--output must be %s or %s, not %q", installOutputText, installOutputJSON, installOutput)
	}
	if err := refuseTerminalUIWithStream(fs); err != nil {
		return nil, err
	}
	s := &installStream{stdout: os.Stdout, presenter: installjson.Presenter{
		Stdout: os.Stdout, RunID: installStreamRunID, Clock: installStreamClock,
	}}
	os.Stdout = os.Stderr
	activeInstallStream = s
	return s, nil
}

// refuseTerminalUIWithStream refuses --output json together with --ui tui, before
// anything runs: one is for a program reading stdout, the other draws on the terminal
// for a person, and both cannot own the same stdout. It looks the flag up rather than
// naming its variable, so it holds from the release that adds --ui on.
func refuseTerminalUIWithStream(fs *pflag.FlagSet) error {
	if f := fs.Lookup("ui"); f != nil && f.Value.String() == "tui" {
		return errors.New("--output json cannot be combined with --ui tui: the event stream is for a program " +
			"reading stdout, the terminal UI for a person at the terminal. Choose one")
	}
	return nil
}

// end gives stdout back and settles the stream: a command refused before its run
// started still writes the one line every stream ends with, a failed summary.
func (s *installStream) end(ctx context.Context, err error) error {
	if s == nil {
		return err
	}
	os.Stdout = s.stdout
	activeInstallStream = nil
	if err != nil && !s.ran {
		if ctx == nil {
			ctx = context.Background()
		}
		if rerr := s.presenter.Refuse(ctx, installflow.OperationInstall, installflow.StepInstallInputs); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

// run runs the install through the stream's presenter.
func (s *installStream) run(ctx context.Context, e *bootstrap.InstallEngine, preflight installflow.PreflightRunner) error {
	s.ran = true
	return s.presenter.Run(ctx, e, installRequest(e), preflight)
}

func installRequest(e *bootstrap.InstallEngine) installflow.Request {
	return installflow.Request{Operation: installflow.OperationInstall, DryRun: e.Options.DryRun, AssumeYes: e.Options.AssumeYes}
}
