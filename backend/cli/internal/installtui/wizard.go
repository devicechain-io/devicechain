// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package installtui

import (
	"maps"

	"github.com/devicechain-io/dcctl/internal/installflow"
)

// Wizard is what the screens before the run show and may change: the welcome screen's
// facts, the cluster choices and the advanced drawer's settings, and how a change to
// any of them becomes a new plan. The command builds it from the request it has already
// parsed and resolved; a presenter without one starts the run as soon as the splash
// ends, as a progress-only run always does.
type Wizard struct {
	Facts Facts
	// Settings are the advanced drawer's rows for the plan as the command line resolved
	// it: the few it can edit, and every other flag the command line gave, carried.
	Settings []Setting
	// Contexts lists the kubeconfig's context names, read from disk and never from a
	// cluster. Nil: the screens offer no context picker.
	Contexts func() ([]string, error)
	// Replan resolves the command line's request with edits applied, through the same
	// resolver the command line uses, and returns the run it becomes. Nil means nothing
	// can be changed here. A refusal (a combination the command line would refuse too)
	// is shown, and the plan stays as it was.
	Replan func(Edits) (Replanned, error)
}

// Edits are the changes the screens have made to the command line's request. Each is
// nil (or absent) while the command line's own value stands, so a value the operator
// never touched keeps the explicitness it had: omitted stays omitted.
type Edits struct {
	// Compact is the footprint: Compact (true) or Standard.
	Compact *bool
	// HA is the topology: HA (true) or Single.
	HA *bool
	// Target is where the install goes.
	Target *Target
	// Toggles are the drawer's editors: a flag's name (without the dashes) and the
	// value it is now given explicitly.
	Toggles map[string]bool
}

// Target is a cluster the install can go to: an existing context, or a local kind
// cluster. A kind target with no name keeps the cluster the command line named.
type Target struct {
	KubeContext string
	Cluster     string
}

func (e Edits) clone() Edits {
	if e.Compact != nil {
		v := *e.Compact
		e.Compact = &v
	}
	if e.HA != nil {
		v := *e.HA
		e.HA = &v
	}
	if e.Target != nil {
		v := *e.Target
		e.Target = &v
	}
	e.Toggles = maps.Clone(e.Toggles)
	return e
}

// Replanned is the run a set of edits resolves to, and how the screens now describe it.
type Replanned struct {
	Engine    installflow.Engine
	Preflight installflow.PreflightRunner
	Facts     Facts
	Settings  []Setting
}

// Setting is one row of the advanced drawer. Every text is sanitised and redacted
// before it is drawn; a credential's value never reaches it (Value says "configured
// (hidden)").
type Setting struct {
	// Group is the drawer section the row belongs to.
	Group string
	// Flag is the flag the row stands for, with its dashes ("--no-monitoring").
	Flag string
	// Label names an editor ("Monitoring stack"); a carried row is named by its flag.
	Label string
	// Value is the setting as it stands, in words.
	Value string
	// Source says where the value came from: "from --no-monitoring", "preset by
	// Compact", "default", "changed here".
	Source string
	// Toggle makes the row an editor: an on/off whose change gives Name the value
	// !FlagValue explicitly. A row without it is carried as the command line gave it.
	Toggle bool
	// Name is the flag's name without dashes, and FlagValue its effective value.
	Name      string
	FlagValue bool
	// Disabled says why an editor cannot be changed in this plan; empty when it can.
	Disabled string
}

// Facts are the resolved request, as the screens state it. They are the command's own
// values, never a guess: an empty field is left off the screen. Every one is sanitised
// and redacted before it is drawn.
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
	// TargetFlag, FootprintFlag and TopologyFlag name the flag that set each, when one
	// did and the screens have not changed it ("--kube-context prod", "--compact",
	// "--size compact", "--ha"); empty when it is the default.
	TargetFlag    string
	FootprintFlag string
	TopologyFlag  string
	// Images is where the images come from, in words.
	Images string
	// SkipPreflight is --skip-preflight: the checks are not run, and the preflight
	// screen says so.
	SkipPreflight bool
}
