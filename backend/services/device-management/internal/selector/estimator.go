// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package selector

import "github.com/google/cel-go/checker"

// MaxAttrElements bounds how many entries the cost estimator assumes the `attr` map holds.
// Without a hint cel-go treats a map as unbounded and every selector that indexes one
// estimates to infinite cost, which would reject even a trivial `attr["climate"] == "arid"`.
// This is a STATIC estimation bound only — it does not cap the runtime map (the lowering
// never materializes attr at all; it emits SQL), it only makes the worst-case cost decidable
// so the publish gate can bound it. A member entity carries a small, bounded set of facets.
const MaxAttrElements = 64

// boundedEstimator gives the declared `attr` map a finite element count so a selector's
// worst-case cost is decidable and gateable at publish. Everything else defers to cel-go's
// defaults (returning nil), mirroring the DETECT predicate estimator.
type boundedEstimator struct{}

// EstimateSize bounds the attr map (and any node typed as it) to MaxAttrElements; all other
// nodes get the default estimate.
func (boundedEstimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	path := node.Path()
	if len(path) > 0 && path[0] == VarAttr {
		return &checker.SizeEstimate{Min: 0, Max: MaxAttrElements}
	}
	return nil
}

// EstimateCallCost defers entirely to cel-go's built-in per-call costs.
func (boundedEstimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	return nil
}
