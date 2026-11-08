// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Impact is the closed vocabulary of how much a successful action changes the physical or
// logical world. It is the WRITE counterpart of Exposure and Scale, which describe a read.
type Impact string

const (
	ImpactNormal      Impact = "normal"
	ImpactDisruptive  Impact = "disruptive"
	ImpactDestructive Impact = "destructive"
)

// ActionRisk is the declaration an ACTION tool must make, the way ToolRisk is for a read.
//
// 🔴 NOTHING USES IT YET. No action tool exists in this build, no scope can authorize one, and
// no authority can carry one (the scope ceilings are pinned to reads). It exists so that the
// ratchet around it is in place BEFORE the first write: registerAction below is the only door
// an action tool may come through, and it is shut.
type ActionRisk struct {
	Impact Impact
	// Reversible says whether the effect can be undone by a later action.
	Reversible bool
	// Discloses is one sentence saying what a successful call changes, for whoever is deciding
	// whether to authorize an agent.
	Discloses string
}

// actuationEnabled is the instance flag that will one day allow action tools to be registered.
// It is a variable only so that a test can prove BOTH branches refuse; there is no code path
// that sets it, and in this build it is false.
var actuationEnabled = false

// errActuationDisabled and errActionsNotBuilt are the two refusals registerAction panics with.
// They are separate strings so a test can tell WHICH check refused: removing the flag check
// must change the message, not only keep the panic.
const (
	errActuationDisabled = "mcp: action tools are disabled (agent actuation is off on this instance)"
	errActionsNotBuilt   = "mcp: action tools are not built in this release; a recognised but unimplemented capability fails loudly"
)

// registerAction is the ONLY way an action tool may be added to the server. It panics, always.
//
// 🔴 FAIL LOUDLY, NEVER RETURN A PLAUSIBLE VALUE. A no-op that quietly skipped the tool would
// look exactly like a working registration at every call site, and the gap would surface
// as a missing capability far from the code that is missing. It runs at process start, so
// the failure is a pod that will not start and a message naming the tool, before anything
// has been added to the server or the catalog.
func registerAction[In, Out any](_ *mcp.Server, _ *Catalog, tool *mcp.Tool, _ ActionRisk,
	_ mcp.ToolHandlerFor[In, Out]) {
	name := ""
	if tool != nil {
		name = tool.Name
	}
	if !actuationEnabled {
		panic(fmt.Sprintf("%s: %q", errActuationDisabled, name))
	}
	panic(fmt.Sprintf("%s: %q", errActionsNotBuilt, name))
}
