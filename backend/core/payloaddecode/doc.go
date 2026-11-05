// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package payloaddecode compiles and runs a tenant-authored payload decoder: a document of
// CEL expressions that maps a binary frame, a JSON body or a network server's decoded object
// into measurements.
//
// # The language
//
// An expression sees frame (bytes), fPort and fCnt (int, -1 off LoRaWAN), json and decoded
// (map(string, dyn)), link (map(string, double)) and the names of the lets before it. It may
// call the owned function library (Functions): byte, bit and BCD readers, slice, hex.decode,
// base64.decode, lpp.decode and the math functions. It has no loops: has() is the only macro,
// and neither a comprehension, a regex nor any string or time function exists in the
// environment. A second fence, the AST walker (allowlist.go), enumerates what is admitted and
// refuses everything else, so a construct a later cel-go release adds stays refused.
//
// # Cost
//
// Every expression has a static worst-case cost (cost.go): cel-go's estimate with a real
// estimator, so a native's cost and the size of what it returns are known, and sizes
// propagate through nested calls and lets. A decoder's estimate composes as
//
//	Σ let + Σ every section's when + max over sections (Σ row when + Σ row expr + map maxKeys)
//
// and both the per-expression (ExprCostCeiling) and the whole-decoder (DecoderCostCeiling)
// ceilings are enforced at compile. The static estimate is ADMISSION. The AUTHORITY is a
// runtime cel.CostLimit set to each expression's estimate: natives report their real cost, and
// an expression that spends more than it declared stops as cost_exceeded. A native Go call is
// not interruptible by that limit, so every native refuses input over its cap before it does
// any work.
//
// # Determinism
//
// A decode is a pure function of (input, document, env version): no clock, no randomness, map
// outputs emitted in sorted key order, math.pow with an integer exponent only (a fractional
// power differs by an ulp between amd64 and arm64), and every double-producing function
// returns through finiteDouble. A function's semantics never change once an env version ships.
//
// # Failure
//
// Decode returns a *DecodeError and no partial readings. The error's text carries only the
// reason, the expression path, the function name and the offset, so it is safe to show a
// tenant.
package payloaddecode
