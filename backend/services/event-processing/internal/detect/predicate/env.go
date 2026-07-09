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
)

// SchemaVersion is the version of the declared event shape. It keys the compiled-program
// cache (a rule recompiles when the schema version it was checked against changes) and is
// the seam a future additive field (a new declared variable) bumps. Bump on any change to
// the declarations below.
const SchemaVersion = 1

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
		)
	})
	if sharedEnvErr != nil {
		return nil, fmt.Errorf("build DETECT predicate environment (schema v%d): %w", SchemaVersion, sharedEnvErr)
	}
	return sharedEnv, nil
}
