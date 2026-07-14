// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

const (
	// DefaultCostCeiling is the platform-default static worst-case CEL cost a selector
	// may estimate to. A caller resolving a per-tenant override MUST pass the resolved
	// ceiling; a missing/zero override maps to this default here, NEVER to "unlimited"
	// (the ADR-023 fail-safe posture, mirroring event-processing's predicate ceiling).
	DefaultCostCeiling uint64 = 100

	// MaxSelectorLeaves caps the number of facet comparisons a selector may carry — i.e.
	// the number of EXISTS semi-joins the lowered query will run. It is the fail-closed
	// bound (analogous to DETECT's MaxActionsPerRule) that keeps a single selector's
	// resolved query from fanning out to an unbounded join count.
	MaxSelectorLeaves = 32
)

// Selector is a compiled, cost-gated, guaranteed-lowerable dynamic-group membership
// predicate. It retains the type-checked AST (lowered to SQL per resolution by Lower) and
// the metadata the publish path stamps onto the group. It carries no live cel.Program —
// production never evaluates the selector as CEL; it lowers to SQL. (The oracle test
// builds a program from the AST to prove the lowering agrees with CEL semantics.)
type Selector struct {
	source     string
	ast        *cel.Ast
	memberType string
	costMax    uint64
	leaves     int
}

// Source is the CEL text the selector compiled from.
func (s *Selector) Source() string { return s.source }

// MemberType is the entity family the selector was compiled for.
func (s *Selector) MemberType() string { return s.memberType }

// CostMax is the static worst-case cost that cleared the publish ceiling.
func (s *Selector) CostMax() uint64 { return s.costMax }

// Leaves is the number of facet comparisons (EXISTS semi-joins) the selector lowers to.
func (s *Selector) Leaves() int { return s.leaves }

// Compile parses, type-checks, cost-gates, and proves-lowerable a dynamic-group selector
// against the shared selector environment for the given member family. It fails closed: a
// parse/type error, a non-boolean result, a worst-case cost above costCeiling, a node
// outside the facet-predicate subset, or more than MaxSelectorLeaves facet leaves all
// reject the selector at publish with a console-surfaceable message. A selector that clears
// Compile is guaranteed to lower to an indexed SQL predicate (Lower).
//
// costCeiling is the per-tenant ceiling resolved by the caller; a zero value resolves to
// DefaultCostCeiling (never unlimited).
func Compile(source, memberType string, costCeiling uint64) (*Selector, error) {
	if costCeiling == 0 {
		costCeiling = DefaultCostCeiling
	}
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
			Err:    fmt.Errorf("a selector must evaluate to a boolean, got %s", ast.OutputType()),
		}
	}
	est, err := env.EstimateCost(ast, boundedEstimator{})
	if err != nil {
		return nil, &CompileError{Source: source, Err: fmt.Errorf("estimate cost: %w", err)}
	}
	if est.Max > costCeiling {
		return nil, &CostError{Source: source, EstimatedMax: est.Max, Ceiling: costCeiling}
	}

	// Walk the checked AST through the exact lowering code path — with throwaway bind
	// values, since only node shapes matter here — so "passes the gate" is literally
	// "Lower will not error". This both rejects any non-lowerable node and counts leaves.
	leaves, err := validateLowerable(ast, source, memberType)
	if err != nil {
		return nil, err
	}
	if leaves > MaxSelectorLeaves {
		return nil, &NotLowerableError{
			Source: source,
			Reason: fmt.Sprintf("carries %d facet comparisons, exceeding the limit of %d", leaves, MaxSelectorLeaves),
		}
	}

	return &Selector{source: source, ast: ast, memberType: memberType, costMax: est.Max, leaves: leaves}, nil
}

// validateLowerable runs the lowering walk purely to (a) reject any node outside the
// facet-predicate subset and (b) count facet leaves. The bind values are placeholders —
// only the AST shape is inspected — and it shares Lower's code path so the gate and the
// emitter can never disagree about what is lowerable.
func validateLowerable(ast *cel.Ast, source, memberType string) (int, error) {
	lc := &lowerCtx{p: LowerParams{
		MemberType:  memberType,
		MemberTable: "_validate_",
		FacetScope:  "_",
		NumericCast: func(col string) string { return col },
	}}
	if _, err := lowerExpr(ast.NativeRep().Expr(), lc); err != nil {
		if nl, ok := err.(*NotLowerableError); ok && nl.Source == "" {
			nl.Source = source
		}
		return 0, err
	}
	return lc.leaves, nil
}
