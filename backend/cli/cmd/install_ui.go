// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/devicechain-io/dcctl/bootstrap"
	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installplain"
	"github.com/devicechain-io/dcctl/internal/installtui"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The presenter flags. --ui defaults to plain: without it, every existing invocation —
// on a terminal or off one — prints, prompts and exits exactly as it always has.
var (
	installUI       string
	installNoSplash bool
)

const (
	installUIUsage = "how the install presents itself: plain (the text output dcctl has always printed), " +
		"tui (a full-screen terminal UI, when stdin, stdout and stderr are all an interactive terminal; " +
		"otherwise plain, unchanged), or auto (the terminal UI on an interactive terminal, and plain " +
		"everywhere else, including with --yes or --dev). --dry-run is always plain"
	installNoSplashUsage = "with the terminal UI: skip the DeviceChain logo shown before the install starts"
)

// parseInstallUI refuses a --ui value that is not one of the three, before anything is
// printed or touched.
func parseInstallUI(v string) (terminal.UI, error) {
	switch ui := terminal.UI(v); ui {
	case terminal.Plain, terminal.TUI, terminal.Auto:
		return ui, nil
	}
	return "", fmt.Errorf("--ui must be plain, tui or auto (got %q)", v)
}

// installUIRequest is what the presenter choice reads from the command line.
type installUIRequest struct {
	UI                         terminal.UI
	Yes, Dev, DryRun, NoSplash bool
}

// restoreProbedConsole puts back a console mode the probe could not restore at the time.
var restoreProbedConsole = installtui.RestoreProbedConsole

// probeInstallTerminal observes the process's streams. Tests replace it.
var probeInstallTerminal = func() terminal.Inputs { return installtui.Probe(os.Stdin, os.Stdout, os.Stderr) }

// selectInstallPresenter is the presenter selection table:
//
//	--ui           terminal                                  presenter
//	plain (default) anything                                  plain, byte for byte as today
//	tui            stdin, stdout and stderr all a terminal,   terminal UI (progress-only with
//	               not CI, TERM not dumb, a VT console          --yes or --dev)
//	tui            anything else                              plain, byte for byte
//	auto           as tui, without --yes or --dev             terminal UI
//	auto           with --yes or --dev, or not eligible       plain, byte for byte
//	any            --dry-run                                  plain (today's rehearsal)
//
// 🔴 PLAIN NEVER PROBES. --ui=plain returns before the terminal is looked at, so the
// default path cannot be changed by anything the probe does or finds.
func selectInstallPresenter(r installUIRequest, probe func() terminal.Inputs) (terminal.Capabilities, bool) {
	if r.UI != terminal.TUI && r.UI != terminal.Auto {
		return terminal.Capabilities{}, false
	}
	// auto is the default-to-be: it must leave every unattended run exactly as it is.
	// --yes (and --dev, which implies it) is how a script runs install, so auto keeps it
	// plain; --ui=tui --yes asks for the progress-only screen explicitly.
	if r.UI == terminal.Auto && (r.Yes || r.Dev) {
		return terminal.Capabilities{}, false
	}
	in := probe()
	in.UI, in.Yes, in.Dev, in.DryRun, in.NoSplash = r.UI, r.Yes, r.Dev, r.DryRun, r.NoSplash
	caps := terminal.Evaluate(in)
	return caps, caps.TUI
}

// installPresenter runs one install and renders it: installplain.Presenter or
// installtui.Presenter.
type installPresenter interface {
	Run(ctx context.Context, e installflow.Engine, req installflow.Request, preflight installflow.PreflightRunner) error
}

// newInstallTUI builds the terminal-UI presenter on the process's own streams. Tests
// replace it, so that the choice can be checked without a terminal to take over.
var newInstallTUI = func(caps terminal.Capabilities) installPresenter {
	// It owns the process's streams while the screen is up: a credential plugin or any
	// other child that reaches them writes to the log pane, never over the screen, and
	// finds no terminal to read.
	return installtui.Presenter{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Caps: caps, OwnProcessStreams: true}
}

// installPresenterFor settles which presenter renders this install, from --ui,
// --no-splash, --dev and the options the run was resolved to.
func installPresenterFor(opts bootstrap.InstallOptions) (installPresenter, error) {
	ui, err := parseInstallUI(installUI)
	if err != nil {
		return nil, err
	}
	if caps, ok := selectInstallPresenter(installUIRequest{UI: ui, Yes: opts.AssumeYes, Dev: installDev,
		DryRun: opts.DryRun, NoSplash: installNoSplash}, probeInstallTerminal); ok {
		return newInstallTUI(caps), nil
	}
	return installplain.Presenter{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}, nil
}

func init() {
	installCmd.Flags().StringVar(&installUI, "ui", string(terminal.Plain), installUIUsage)
	installCmd.Flags().BoolVar(&installNoSplash, "no-splash", false, installNoSplashUsage)
}
