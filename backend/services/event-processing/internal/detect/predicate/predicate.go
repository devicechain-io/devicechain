// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"fmt"
	"time"

	"github.com/google/cel-go/cel"
)

// Input is one resolved event as seen by a predicate: the neutral view the runtime builds
// from a device-management ResolvedEvent (kept free of that dependency so this leaf layer
// stays importable everywhere). M holds only the numeric, bound measurements of the event;
// Attr holds the device's OWN durable numeric attributes (dynamic-threshold source), which
// come from the device-attribute projection, NOT the event.
type Input struct {
	Device   string
	Anchors  map[string]string
	Occurred time.Time
	M        map[string]float64
	Attr     map[string]float64
}

// activation renders the input as the CEL variable bindings. A nil map is passed as an
// empty map so `"x" in m` (or `"x" in attr`) is a clean false rather than an evaluation error.
func (in Input) activation() map[string]any {
	anchors := in.Anchors
	if anchors == nil {
		anchors = map[string]string{}
	}
	m := in.M
	if m == nil {
		m = map[string]float64{}
	}
	attr := in.Attr
	if attr == nil {
		attr = map[string]float64{}
	}
	return map[string]any{
		VarDevice:   in.Device,
		VarAnchors:  anchors,
		VarOccurred: in.Occurred,
		VarM:        m,
		VarAttr:     attr,
	}
}

// Predicate is a compiled, reusable boolean leaf condition. It is compiled once per rule
// version (compile-once) and evaluated against every event for that rule; cel.Program is
// safe for concurrent evaluation, but DETECT's single-writer loop calls it serially.
type Predicate struct {
	source  string
	program cel.Program
	// costMax is the static worst-case cost estimate that passed the publish-time gate,
	// retained so the runtime can surface it (rule-health, operator budget view).
	costMax uint64
}

// Source is the CEL text the predicate compiled from (generated or raw). Useful for
// round-trip tests and operator introspection.
func (p *Predicate) Source() string { return p.source }

// CostMax is the static worst-case cost that cleared the publish-time ceiling.
func (p *Predicate) CostMax() uint64 { return p.costMax }

// Compile parses, type-checks, and cost-gates a CEL boolean expression against the shared
// environment, returning a reusable Predicate. It fails closed: a parse/type error, a
// non-boolean result, or a worst-case cost above costCeiling all reject the rule at
// publish with a message the console can surface. The returned Program also carries a
// runtime CostLimit at the same ceiling as a backstop against an under-estimate.
//
// costCeiling is the per-tenant ceiling resolved by the caller (a missing/zero tenant
// override must resolve to the platform default before this call — never to "unlimited",
// per the ADR-023 fail-safe posture).
func Compile(source string, costCeiling uint64) (*Predicate, error) {
	env, err := Env()
	if err != nil {
		return nil, err
	}
	ast, iss := env.Compile(source)
	if iss != nil && iss.Err() != nil {
		return nil, &CompileError{Source: source, Err: iss.Err()}
	}
	if ast.OutputType() != cel.BoolType {
		return nil, &CompileError{
			Source: source,
			Err:    fmt.Errorf("predicate must evaluate to a boolean, got %s", ast.OutputType()),
		}
	}
	est, err := env.EstimateCost(ast, boundedEstimator{})
	if err != nil {
		return nil, &CompileError{Source: source, Err: fmt.Errorf("estimate cost: %w", err)}
	}
	if est.Max > costCeiling {
		return nil, &CostError{Source: source, EstimatedMax: est.Max, Ceiling: costCeiling}
	}
	program, err := env.Program(ast, cel.CostLimit(costCeiling))
	if err != nil {
		return nil, &CompileError{Source: source, Err: fmt.Errorf("build program: %w", err)}
	}
	return &Predicate{source: source, program: program, costMax: est.Max}, nil
}

// Eval evaluates the predicate against one event. An evaluation error (e.g. the runtime
// cost limit tripped, or a raw-CEL leaf that indexed a guaranteed-present key that was
// absent) is returned to the caller; the runtime treats an eval error as "did not match"
// but counts it, so a persistently-erroring rule is visible rather than silently dead.
func (p *Predicate) Eval(in Input) (bool, error) {
	out, _, err := p.program.Eval(in.activation())
	if err != nil {
		return false, err
	}
	b, ok := out.Value().(bool)
	if !ok {
		// The env guarantees bool output at compile; this is an unreachable belt-and-braces.
		return false, fmt.Errorf("predicate produced a non-boolean %T at runtime", out.Value())
	}
	return b, nil
}
