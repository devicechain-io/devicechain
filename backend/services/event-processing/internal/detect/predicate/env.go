// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package predicate is DETECT's leaf-condition layer: it compiles the boolean CEL
// expression a rule's leaf lowers to (ADR-051 slice 3) against one shared, versioned
// CEL environment declaring the resolved-event shape, and evaluates it against a single
// event to produce the Match bit the keyed-streaming core consumes.
//
// The environment is the contract every predicate — structured-generated or the raw-CEL
// escape hatch — is type-checked against, so a rule that references an undeclared field
// or the wrong type is rejected at publish, not at runtime. cel-go's Check enforces that;
// this package adds the cost gate and the boolean-output requirement on top.
package predicate

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

// The variable names the environment declares. They are the whole vocabulary a predicate
// may reference; cel-go's type checker rejects any other identifier. Keep these stable —
// they are part of the authored-rule contract (a published raw-CEL leaf names them).
const (
	// VarDevice is the source device's stable per-tenant token (ADR-044).
	VarDevice = "device"
	// VarAnchors maps a tracked-relationship anchor type to its target token (ADR-013):
	// e.g. anchors["site"] == "site-42". Absent anchor types are simply not keys.
	VarAnchors = "anchors"
	// VarOccurred is the event's occurred timestamp (event time, the watermark's basis).
	VarOccurred = "occurred"
	// VarM maps a measurement name to its numeric value. Measurements are numeric-only by
	// design (ADR-016); non-numeric or unbound measurements are not keys, so a predicate
	// must guard presence (`"temp" in m`) — the structured generator always does.
	VarM = "m"
	// VarAttr maps a device-attribute key to its numeric value — the device's OWN durable
	// state (SERVER/SHARED scope, ADR-012), NOT the current event's readings. It is the source
	// of a dynamic per-device threshold (ADR-051 slice 4c-3b): `m["temp"] > attr["tempLimit"]`.
	// Numeric-only and absent-like `m` — a device that has not set the attribute is simply not a
	// key, so a dynamic comparison must guard presence (`"tempLimit" in attr`), which the
	// structured generator always does. The runtime populates it from the device-attribute
	// projection (slice 4c-3b-2-ii); until then it is always empty.
	//
	// SCOPE OF THE "empty until 2-ii" GUARANTEE. Because the STRUCTURED generator only ever emits
	// a POSITIVE presence guard (`"k" in attr && …`), every structured dynamic rule cleanly does
	// NOT fire while attr is empty — it cannot mis-fire on absent state. A RAW-CEL leaf is not
	// bound by that: an author who writes a NEGATED presence (`!("k" in attr) && …`) gets an
	// always-true guard while attr is empty, so such a raw rule fires as if no device has the
	// attribute, then changes behavior once 2-ii populates the map. This is the same "raw-CEL
	// author owns totality" contract the Duration trap documents (compile.go): the structured
	// path is the safe one; a raw leaf referencing attr owns its own correctness across the go-live.
	VarAttr = "attr"
)

// SchemaVersion is the version of the declared event shape. Nothing consumes it yet: it is
// the seam a future compiled-program cache would key on (recompiling a rule when the schema
// version it was checked against changes), and the marker a future additive field (a new
// declared variable) bumps. Bump on any change to the declarations below.
//
//   - v1: device, anchors, occurred, m.
//   - v2: + attr (device-attribute map, dynamic thresholds, ADR-051 slice 4c-3b).
const SchemaVersion = 2

var (
	sharedEnv     *cel.Env
	sharedEnvErr  error
	sharedEnvOnce sync.Once
)

// Env returns the process-wide shared CEL environment for the current SchemaVersion. One
// env is built once and reused for every rule's Compile and every Program: cel.Env is
// safe for concurrent Program construction and evaluation, and building it is not cheap,
// so it must not be per-rule. It fails closed — a construction error is returned to every
// caller rather than yielding a half-built env.
func Env() (*cel.Env, error) {
	sharedEnvOnce.Do(func() {
		sharedEnv, sharedEnvErr = cel.NewEnv(
			cel.Variable(VarDevice, cel.StringType),
			cel.Variable(VarAnchors, cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable(VarOccurred, cel.TimestampType),
			cel.Variable(VarM, cel.MapType(cel.StringType, cel.DoubleType)),
			cel.Variable(VarAttr, cel.MapType(cel.StringType, cel.DoubleType)),
		)
	})
	if sharedEnvErr != nil {
		return nil, fmt.Errorf("build DETECT predicate environment (schema v%d): %w", SchemaVersion, sharedEnvErr)
	}
	return sharedEnv, nil
}
