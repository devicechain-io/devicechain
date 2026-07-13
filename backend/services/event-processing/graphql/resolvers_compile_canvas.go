// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"math"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/rules/graph"
	"github.com/devicechain-io/dc-microservice/auth"
)

// CompileCanvas lowers a visual automation canvas (ADR-053) to its DETECT rule definition —
// the server-authoritative graph→schema pass (slice 9a). It is the trust boundary: the
// browser renders and previews, but the AUTHORITATIVE lowering runs here, so a hostile or
// buggy client can never author a definition that bypasses the compiler + cost gate. It
// reuses the exact rules.Compile path the form validation gate (ValidateDetectionRules) and
// the runtime fact-consumer share, so a canvas rule and a form rule compile identically.
//
// It gates on device:read — the same least-privilege authority as the validation gate (a
// read-only compile over the profile aggregate rules belong to); the compile is pure (no
// state read or written).
func (r *SchemaResolver) CompileCanvas(ctx context.Context, args struct {
	Graph        string
	ProfileToken string
}) (*CanvasCompileResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	def, err := graph.Decode([]byte(args.Graph))
	if err != nil {
		// A malformed graph is a graph-level (node-less) diagnostic, not a transport error —
		// the console surfaces it the same way as a compile rejection.
		return failedCanvas(graph.Diagnostic{Severity: "error", Message: err.Error()}), nil
	}

	// Platform-default compile limits, shared verbatim with the validation gate and the
	// runtime consumer via rules.DefaultLimits so a canvas rule that compiles here compiles
	// identically everywhere (never uncapped — ADR-023).
	res, cerr := graph.Compile(def, args.ProfileToken, rules.DefaultLimits())
	if cerr != nil {
		var ce *graph.CompileError
		if errors.As(cerr, &ce) {
			return failedCanvas(ce.Diagnostics...), nil
		}
		return failedCanvas(graph.Diagnostic{Severity: "error", Message: cerr.Error()}), nil
	}

	// The GA profile-homed canvas maps to exactly one DetectionRule (one Definition + one
	// AuthoringGraph sidecar, §4.1). The lowering itself supports N condition nodes → N rules,
	// but this surface is tied to the one-rule storage, so a graph with zero or many
	// conditions is rejected with a graph-level diagnostic rather than silently dropping rules.
	if len(res.Rules) != 1 {
		return failedCanvas(graph.Diagnostic{
			Severity: "error",
			Message:  "a canvas authors exactly one detection rule — this graph has more than one condition node; split it into separate rules",
		}), nil
	}

	lr := res.Rules[0]
	def0 := lr.Definition
	cost := clampCost(lr.Compiled.Predicate.CostMax())
	return &CanvasCompileResultResolver{
		ok:         true,
		definition: &def0,
		cost:       &cost,
		diags:      nil,
	}, nil
}

// clampCost narrows a predicate cost (uint64) to the GraphQL Int range, saturating rather
// than wrapping a pathological value.
func clampCost(v uint64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// failedCanvas builds an unsuccessful result carrying the given diagnostics.
func failedCanvas(diags ...graph.Diagnostic) *CanvasCompileResultResolver {
	rs := make([]*CanvasDiagnosticResolver, 0, len(diags))
	for _, d := range diags {
		rs = append(rs, newCanvasDiagnostic(d))
	}
	return &CanvasCompileResultResolver{ok: false, diags: rs}
}

// CanvasCompileResultResolver resolves the canvas compile outcome.
type CanvasCompileResultResolver struct {
	ok         bool
	definition *string
	cost       *int32
	diags      []*CanvasDiagnosticResolver
}

// Ok reports whether the graph compiled to exactly one rule that passed the cost gate.
func (r *CanvasCompileResultResolver) Ok() bool { return r.ok }

// Definition resolves the compiled rules.Rule JSON (null when !ok).
func (r *CanvasCompileResultResolver) Definition() *string { return r.definition }

// EstimatedCost resolves the compiled predicate's worst-case cost (null when !ok).
func (r *CanvasCompileResultResolver) EstimatedCost() *int32 { return r.cost }

// Diagnostics resolves the node-anchored problems (empty when ok).
func (r *CanvasCompileResultResolver) Diagnostics() []*CanvasDiagnosticResolver {
	return r.diags
}

// newCanvasDiagnostic maps a graph.Diagnostic to its resolver, carrying a null nodeId for a
// graph-level problem.
func newCanvasDiagnostic(d graph.Diagnostic) *CanvasDiagnosticResolver {
	res := &CanvasDiagnosticResolver{severity: d.Severity, message: d.Message}
	if d.NodeID != "" {
		id := d.NodeID
		res.nodeID = &id
	}
	return res
}

// CanvasDiagnosticResolver resolves one canvas diagnostic.
type CanvasDiagnosticResolver struct {
	nodeID   *string
	severity string
	message  string
}

// NodeId resolves the offending node id (null for a graph-level problem).
func (r *CanvasDiagnosticResolver) NodeId() *string { return r.nodeID }

// Severity resolves the diagnostic severity.
func (r *CanvasDiagnosticResolver) Severity() string { return r.severity }

// Message resolves the console-surfaceable message.
func (r *CanvasDiagnosticResolver) Message() string { return r.message }
