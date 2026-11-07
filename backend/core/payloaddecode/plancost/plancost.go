// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package plancost is the part of a decoder's cost that depends on the size of the uplink it
// decodes, in a package with no dependencies so that ingest can price an uplink without linking
// the expression engine. payloaddecode.Plan.CostFor is built on it, so the price ingest charges
// and the price the decoder is held to are one formula.
package plancost

// The input caps. They live here, in the leaf, because ingest enforces them before it carries an
// input and the decoder enforces them again before it works on one: payloaddecode's MaxFrameBytes,
// MaxJSONBytes and MaxJSONDepth ARE these values.
const (
	MaxFrameBytes = 4096     // the raw frame an uplink may carry
	MaxJSONBytes  = 64 << 10 // the canonical JSON of `json` or `decoded`
	MaxJSONDepth  = 32       // nesting past this is refused before the value is carried
)

// ParseCost is the cost, in the same units as an expression's, of checking and parsing n bytes
// of JSON: one unit a byte, so the largest legal input costs 65,536. See payloaddecode.ParseCost.
func ParseCost(n int) uint64 { return uint64(max(n, 0)) }

// Shape is what a decoder's cost is made of: the composition of its expression estimates, and
// which JSON inputs it reads (each is parsed, and the parse is priced by its actual size).
type Shape struct {
	// Composed is the static cost of the expressions alone.
	Composed uint64
	// ParsesJSON and ParsesDecoded say which input maps the decoder reads.
	ParsesJSON, ParsesDecoded bool
}

// For is the static worst-case cost of decoding an uplink whose JSON and decoded object are the
// given sizes in bytes. It is at most the plan's EstimatedCost, which prices each parse at the
// largest legal input.
func (s Shape) For(jsonLen, decodedLen int) uint64 {
	c := s.Composed
	if s.ParsesJSON {
		c += ParseCost(jsonLen)
	}
	if s.ParsesDecoded {
		c += ParseCost(decodedLen)
	}
	return c
}
