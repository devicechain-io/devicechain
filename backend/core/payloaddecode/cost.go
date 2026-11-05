// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// estimator is the static cost estimator. Unlike every other estimator in the tree it returns
// real numbers: a cost for each owned function, and a ResultSize for each that returns bytes
// or a map, so sizes propagate through nested calls and (via letSizes) through let bindings.
type estimator struct {
	// letSizes is the worst-case size of each bytes-typed let seen so far.
	letSizes map[string]checker.SizeEstimate
}

func (e *estimator) EstimateSize(node checker.AstNode) *checker.SizeEstimate {
	p := node.Path()
	if len(p) != 1 {
		return nil
	}
	switch p[0] {
	case VarFrame:
		return &checker.SizeEstimate{Min: 0, Max: MaxFrameBytes}
	case VarJSON, VarDecoded:
		return &checker.SizeEstimate{Min: 0, Max: MaxJSONBytes}
	case VarLink:
		return &checker.SizeEstimate{Min: 0, Max: 3}
	}
	if sz, ok := e.letSizes[p[0]]; ok {
		return &sz
	}
	return nil
}

func (e *estimator) EstimateCallCost(function, overloadID string, target *checker.AstNode, args []checker.AstNode) *checker.CallEstimate {
	f, ok := byName[function]
	if !ok || target != nil {
		return nil
	}
	cost, size := f.static(args)
	return &checker.CallEstimate{CostEstimate: checker.CostEstimate{Min: cost, Max: cost}, ResultSize: size}
}

// actualCost reports the real cost of an owned function at run time, which cel-go adds to its
// own per-node charges. A function it does not own keeps cel-go's default.
type actualCost struct{}

func (actualCost) CallCost(function, _ string, args []ref.Val, result ref.Val) *uint64 {
	f, ok := byName[function]
	if !ok {
		return nil
	}
	c := f.actual(args, result)
	return &c
}

// bytesBound is the worst-case size of a bytes-typed expression: the size a later expression
// reading it through a let may assume. It is deliberately a small recursive walk over the
// shapes that matter; anything else is the largest bytes value there can be.
func bytesBound(e celast.Expr, letSizes map[string]checker.SizeEstimate) uint64 {
	switch e.Kind() {
	case celast.IdentKind:
		if e.AsIdent() == VarFrame {
			return MaxFrameBytes
		}
		if sz, ok := letSizes[e.AsIdent()]; ok {
			return sz.Max
		}
	case celast.LiteralKind:
		if b, ok := e.AsLiteral().(types.Bytes); ok {
			return uint64(len(b))
		}
	case celast.CallKind:
		c := e.AsCall()
		args := c.Args()
		switch c.FunctionName() {
		case "slice":
			if len(args) == 3 {
				src := bytesBound(args[0], letSizes)
				if args[2].Kind() == celast.LiteralKind {
					if n, ok := args[2].AsLiteral().(types.Int); ok && n >= 0 && uint64(n) < src {
						return uint64(n)
					}
				}
				return src
			}
		case "hex.decode", "base64.decode":
			if len(args) == 1 && args[0].Kind() == celast.LiteralKind {
				if s, ok := args[0].AsLiteral().(types.String); ok {
					if c.FunctionName() == "hex.decode" {
						return uint64(len(s)) / 2
					}
					return min(uint64(len(s))/4*3, MaxFrameBytes)
				}
			}
		case operators.Conditional:
			if len(args) == 3 {
				return max(bytesBound(args[1], letSizes), bytesBound(args[2], letSizes))
			}
		}
	}
	return MaxFrameBytes
}
