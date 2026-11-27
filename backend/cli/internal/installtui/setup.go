// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/devicechain-io/dcctl/internal/installflow"
	"github.com/devicechain-io/dcctl/internal/installtui/terminal"
)

// The exit statuses of dcctl setup local. Each names what the command leaves behind,
// so a caller can act on the status alone.
//
//	0  the cluster is installed and the instance was created
//	1  the cluster stage failed, or was interrupted after its approval: exactly what a
//	   failed dcctl install leaves, and its status. The cluster may be partly changed.
//	2  nothing was changed: stopped before the cluster's approval, or refused before
//	   anything started (no interactive terminal, a bad argument)
//	3  the cluster is installed and stays installed; the instance was not created,
//	   because the instance stage was left before its approval
//	4  the cluster is installed and stays installed; the instance stage failed, or was
//	   interrupted after its approval
//
// 0 and 1 mean what they mean for dcctl install. 3 and 4 are new because no single
// command before setup could leave a cluster installed and an instance not.
const (
	SetupExitSucceeded          = 0
	SetupExitClusterFailed      = 1
	SetupExitNothingChanged     = 2
	SetupExitInstanceNotCreated = 3
	SetupExitInstanceFailed     = 4
)

// PhaseOutcome is how one stage of dcctl setup local ended.
type PhaseOutcome uint8

const (
	// PhaseNotReached: the stage was never offered (an earlier one did not succeed).
	PhaseNotReached PhaseOutcome = iota
	// PhaseNotApproved: the stage's screens ended before its approval. Nothing of it ran.
	PhaseNotApproved
	// PhaseFailed: the stage was approved, then failed or was interrupted.
	PhaseFailed
	// PhaseSucceeded: the stage was approved and finished.
	PhaseSucceeded
)

// SetupResult is how each stage of a setup ended.
type SetupResult struct {
	Cluster, Instance PhaseOutcome
	// Err is the error of the stage that decided the result, nil on success.
	Err error
}

// ExitCode is the status dcctl setup local exits with: see SetupExitSucceeded.
func (r SetupResult) ExitCode() int {
	switch r.Cluster {
	case PhaseSucceeded:
	case PhaseFailed:
		return SetupExitClusterFailed
	default:
		return SetupExitNothingChanged
	}
	switch r.Instance {
	case PhaseSucceeded:
		return SetupExitSucceeded
	case PhaseFailed:
		return SetupExitInstanceFailed
	}
	return SetupExitInstanceNotCreated
}

// phaseOf is a stage's outcome from its presenter's error: nil is success, a run nobody
// approved is ErrNotConfirmed, and anything else failed.
func phaseOf(err error) PhaseOutcome {
	switch {
	case err == nil:
		return PhaseSucceeded
	case errors.Is(err, ErrNotConfirmed):
		return PhaseNotApproved
	}
	return PhaseFailed
}

// Setup is dcctl setup local: the cluster journey, then the instance journey, each
// its own run with its own approval, on one terminal.
//
// 🔴 THE INSTANCE STAGE STARTS ONLY AFTER THE CLUSTER STAGE SUCCEEDED, and in a
// presenter of its own: a new program, a new bridge, a new prompt broker. Nothing the
// cluster's review approved can answer anything the instance run asks, and its screens
// begin on the hand-over, which settles before it takes a key.
type Setup struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Caps   terminal.Capabilities
	// OwnProcessStreams is passed to each stage's presenter.
	OwnProcessStreams bool

	// The cluster stage: the install's own engine, request, preflight and screens.
	Cluster          installflow.Engine
	ClusterRequest   installflow.Request
	ClusterPreflight installflow.PreflightRunner
	Wizard           *Wizard

	// Instance builds the instance stage's screens once the cluster stage has
	// succeeded, from what it installed. Their Handover is shown first.
	Instance func() *BootstrapScreens
	// ContinueCommand is the command that creates the instance later, by hand: from
	// the choices when they were settled (planned is non-nil), or a template.
	ContinueCommand func(planned *BootstrapPlanned) string

	// run is a test seam: it runs one stage's presenter. Nil runs it.
	run func(ctx context.Context, p Presenter, e installflow.Engine, req installflow.Request,
		preflight installflow.PreflightRunner) error
}

func (s Setup) runStage(ctx context.Context, p Presenter, e installflow.Engine, req installflow.Request,
	preflight installflow.PreflightRunner) error {
	if s.run != nil {
		return s.run(ctx, p, e, req, preflight)
	}
	return p.Run(ctx, e, req, preflight)
}

// Run runs the stages in order and returns how each ended. It prints what the
// terminal is left with: after the instance stage did not succeed, that the cluster
// stays installed and the exact command that creates the instance later.
func (s Setup) Run(ctx context.Context) SetupResult {
	var res SetupResult
	cluster := Presenter{Stdin: s.Stdin, Stdout: s.Stdout, Stderr: s.Stderr, Caps: s.Caps,
		OwnProcessStreams: s.OwnProcessStreams, Wizard: s.Wizard, Continues: true}
	err := s.runStage(ctx, cluster, s.Cluster, s.ClusterRequest, s.ClusterPreflight)
	res.Cluster, res.Err = phaseOf(err), err
	if res.Cluster != PhaseSucceeded {
		return res
	}

	var planned *BootstrapPlanned
	scr := *s.Instance()
	chosen := scr.Chosen
	scr.Chosen = func(p BootstrapPlanned) {
		planned = &p
		if chosen != nil {
			chosen(p)
		}
	}
	if ctx.Err() != nil {
		// Interrupted between the stages: the instance stage is not begun.
		res.Instance, res.Err = PhaseNotApproved, ctx.Err()
	} else {
		// The logo was the cluster stage's; the instance stage opens on the hand-over.
		caps := s.Caps
		caps.Splash, caps.Animate, caps.ProgressOnly = terminal.NoArt, false, false
		instance := Presenter{Stdin: s.Stdin, Stdout: s.Stdout, Stderr: s.Stderr, Caps: caps,
			OwnProcessStreams: s.OwnProcessStreams, Bootstrap: &scr}
		err = s.runStage(ctx, instance, nil,
			installflow.Request{Operation: installflow.OperationBootstrap}, nil)
		res.Instance, res.Err = phaseOf(err), err
	}
	if res.Instance == PhaseSucceeded {
		return res
	}
	next := ""
	if s.ContinueCommand != nil {
		next = s.ContinueCommand(planned)
	}
	switch res.Instance {
	case PhaseFailed:
		fmt.Fprintln(s.Stdout, "dcctl setup: the cluster is installed and stays installed; creating the instance failed.")
		fmt.Fprintln(s.Stdout, "Fix the cause, then create the instance with:")
	default:
		fmt.Fprintln(s.Stdout, "dcctl setup: the cluster is installed and stays installed; no instance was created.")
		fmt.Fprintln(s.Stdout, "Create one later with:")
	}
	if next != "" {
		fmt.Fprintln(s.Stdout, "  "+next)
	}
	return res
}
