// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package graph is the visual automation canvas (ADR-053) authoring model and its
// server-authoritative lowering onto the DETECT rule schema (ADR-051). A canvas is
// authoring-time only: it adds ONE server compiler pass (graph → rules.Rule) and NO new
// runtime — a canvas rule and a form rule (slice 7) that express the same logic compile to
// a byte-identical rules.Rule, because the canvas is a projection onto the same schema and
// the same rules.Compile + cost gate. Slice 9a lands the DETECT half (Source + the seven
// condition nodes + the plain Action attachment); the conditional/enriched REACT chain
// (branch/enrich) and the compute node are follow-ups (slices 9c / 9a-follow-up).
//
// The load-bearing idea is the typed port system (§1 of the slice-9 spec): every port
// carries exactly one of three signals and an edge may only join same-typed ports. A
// condition node is the ONLY node with a stream input and a signal output, so the
// DETECT↔REACT boundary is mechanically checkable rather than a convention — everything
// transitively upstream of a condition lowers to DETECT, everything downstream to REACT.
package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// SchemaVersion is the only CanvasDefinition schema version this build understands. A
// definition carrying any other version is rejected (forward-compat gate) rather than
// silently mis-parsed — the same fail-closed posture the rule decoder takes.
const SchemaVersion = 1

// PortType is the typed signal a port carries. Edges may only connect same-typed ports;
// the type transition at a condition node is what makes the DETECT↔REACT partition provable.
type PortType string

const (
	// PortStream carries the ordered ResolvedEvent stream (DETECT half).
	PortStream PortType = "stream"
	// PortSignal carries an edge-triggered detection signal (the DETECT→REACT boundary).
	PortSignal PortType = "signal"
	// PortValue carries a CEL-typed scalar/struct from a compute node (either half). No 9a
	// node produces or consumes it yet; it is reserved so the type system does not have to
	// change when the compute node lands.
	PortValue PortType = "value"
)

// NodeType is the discriminator for a canvas node. The 9a catalog is the DETECT half plus
// the plain Action node; compute/branch/enrich are added by later slices (an unknown type
// is rejected, so a canvas from a newer editor fails closed here rather than mis-lowering).
type NodeType string

const (
	NodeSource      NodeType = "source"
	NodeThreshold   NodeType = "threshold"
	NodeDuration    NodeType = "duration"
	NodeAbsence     NodeType = "absence"
	NodeAggregate   NodeType = "aggregate"
	NodeDeltaRate   NodeType = "deltaRate"
	NodeRepeating   NodeType = "repeating"
	NodeCorrelation NodeType = "correlation"
	// NodeBranch is a REACT-side router (ADR-053 slice 9c): a signal→signal node carrying a CEL
	// boolean that gates the actions downstream of it. It lowers to NO runtime node — its predicate
	// is folded onto the Guard of every action reachable through it (see lower.go), so a branch is
	// pure authoring sugar over rules.Action.Guard, not a new engine primitive (ADR-054).
	NodeBranch NodeType = "branch"
	NodeAction NodeType = "action"
)

// ports describes a node type's typed input and output ports, keyed by port name. The
// lowering resolves an edge endpoint "nodeId:port" against this table to type-check the
// edge and to walk the stream-upstream / signal-downstream subgraphs.
type ports struct {
	in  map[string]PortType
	out map[string]PortType
}

// category groups node types for the partition. A condition node is the pivot: the only
// node with a stream input AND a signal output.
type category int

const (
	catSource category = iota
	catCondition
	catBranch
	catAction
)

// catalog is the GA node catalog (9a subset). It is the single source of truth for a node
// type's ports and category; a type absent here is unknown and rejected at decode.
var catalog = map[NodeType]struct {
	cat   category
	ports ports
}{
	NodeSource: {catSource, ports{out: map[string]PortType{"out": PortStream}}},
	NodeThreshold: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeDuration: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeAbsence: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeAggregate: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeDeltaRate: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeRepeating: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeCorrelation: {catCondition, ports{
		in:  map[string]PortType{"in": PortStream},
		out: map[string]PortType{"signal": PortSignal},
	}},
	NodeBranch: {catBranch, ports{
		in:  map[string]PortType{"in": PortSignal},
		out: map[string]PortType{"out": PortSignal},
	}},
	NodeAction: {catAction, ports{in: map[string]PortType{"in": PortSignal}}},
}

// isCondition reports whether a node type is a condition node (stream→signal pivot).
func (t NodeType) isCondition() bool {
	c, ok := catalog[t]
	return ok && c.cat == catCondition
}

// isBranch reports whether a node type is a branch node (signal→signal router, slice 9c).
func (t NodeType) isBranch() bool {
	c, ok := catalog[t]
	return ok && c.cat == catBranch
}

// CanvasDefinition is the authored graph: a versioned set of typed nodes and the edges
// wiring their ports. It round-trips through @xyflow/react on the frontend; the ui field on
// each node is layout-only and never reaches the compiler, so a re-laid-out graph compiles
// identically.
type CanvasDefinition struct {
	SchemaVersion int    `json:"schemaVersion"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

// Node is one canvas node: a stable id, its type, its opaque per-type config (decoded
// against the type's config struct with unknown-field rejection during lowering), and
// authoring-only layout coordinates.
type Node struct {
	ID     string          `json:"id"`
	Type   NodeType        `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
	UI     *NodeUI         `json:"ui,omitempty"`
}

// NodeUI is authoring-only layout; the compiler ignores it.
type NodeUI struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge wires one output port to one input port, each addressed "nodeId:port".
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Decode parses a CanvasDefinition, failing closed on any unknown field or trailing content
// (the project's reject-unknown-keys convention) so a malformed or forward-versioned graph
// is rejected rather than silently mis-parsed. Per-node config is deferred (json.RawMessage)
// and decoded against its type's struct — also fail-closed — during lowering.
func Decode(data []byte) (CanvasDefinition, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d CanvasDefinition
	if err := dec.Decode(&d); err != nil {
		return CanvasDefinition{}, fmt.Errorf("decode canvas: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return CanvasDefinition{}, fmt.Errorf("decode canvas: unexpected trailing content after the canvas object")
	}
	return d, nil
}

// decodeConfig decodes one node's opaque config against its per-type struct, failing closed
// on an unknown or stray field — the guard that catches an editor emitting a parameter onto
// the wrong node type (mirrors rules.Decode's posture for the rule blob).
func decodeConfig(raw json.RawMessage, dst interface{}) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing content in node config")
	}
	return nil
}
