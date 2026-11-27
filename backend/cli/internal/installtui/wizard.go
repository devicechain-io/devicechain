// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import "github.com/devicechain-io/dcctl/internal/installflow"

// Wizard is what the screens before the run show and may change: the welcome screen's
// facts, and the one change the preflight screen can make to the plan (the footprint).
// The command builds it from the request it has already parsed and resolved; a
// presenter without one starts the run as soon as the splash ends, as a progress-only
// run always does.
type Wizard struct {
	Facts Facts
	// Replan resolves the same request with the other footprint, through the resolver
	// the command line uses, and returns the run it becomes: the engine, and the
	// preflight that engine runs. Nil means the footprint cannot be changed here. A
	// refusal (a combination the command line would refuse too) is shown, and the
	// plan stays as it was.
	Replan func(compact bool) (installflow.Engine, installflow.PreflightRunner, error)
}

// Facts are the resolved request, as the welcome screen states it. They are the
// command's own values, never a guess: an empty field is left off the screen. Every one
// is sanitised and redacted before it is drawn.
type Facts struct {
	// Version is the dcctl version, shown in the header and on the splash.
	Version string
	// Provider is the provider named on the command line; Providers is every one this
	// build registers (bootstrap.Names), the only ones a screen may offer.
	Provider  string
	Providers []string
	// KubeContext is the existing cluster's context. Empty: a local kind cluster named
	// Cluster, created if it does not exist.
	KubeContext string
	Cluster     string
	Compact     bool
	HA          bool
	// FootprintFlag and TopologyFlag name the flag that set each, when one did
	// ("--compact", "--size compact", "--ha"); empty when it is the default.
	FootprintFlag string
	TopologyFlag  string
	// Images is where the images come from, in words.
	Images string
	// SkipPreflight is --skip-preflight: the checks are not run, and the preflight
	// screen says so.
	SkipPreflight bool
}
