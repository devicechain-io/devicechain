// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import "github.com/google/cel-go/checker"

// MaxMapElements bounds how many entries the cost estimator assumes the input maps (m,
// anchors) hold. A resolved event carries a small, bounded set of measurements and
// anchors; without a hint cel-go treats a map as unbounded and every predicate that
// iterates or indexes one estimates to an infinite cost, which would reject even trivial
// rules. This is a *static estimation* bound only — it does not cap the runtime map; the
// per-tenant ingest limits (ADR-023) bound the actual event size, and the CostLimit set on
// the Program is the runtime backstop.
const MaxMapElements = 64

// boundedEstimator is a checker.CostEstimator that gives the declared input maps a finite
// element-count so a predicate's worst-case cost is decidable and can be gated at publish.
// Everything else defers to cel-go's defaults (returning nil).
type boundedEstimator struct{}

// EstimateSize bounds the two map variables (and any node typed as one of them) to
// MaxMapElements; all other nodes get the default estimate.
func (boundedEstimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	path := node.Path()
	if len(path) > 0 {
		switch path[0] {
		case VarM, VarAnchors:
			return &checker.SizeEstimate{Min: 0, Max: MaxMapElements}
		}
	}
	return nil
}

// EstimateCallCost defers entirely to cel-go's built-in per-call costs.
func (boundedEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	return nil
}
