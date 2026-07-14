// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package selector is device-management's dynamic-group membership engine (ADR-061
// G3, SD-3). A dynamic EntityGroup stores its membership as one CEL string over the
// member entity's facet attributes; this package compiles + cost-gates that string
// at publish and lowers its type-checked AST to an indexed SQL predicate over
// entity_attributes, so members resolve as a correctly-paginated query rather than
// a per-row CEL evaluation (the eval-on-read path).
//
// It deliberately MIRRORS event-processing/internal/detect/predicate — the same
// built-once/fail-closed env, static-cost + runtime-CostLimit gate, and
// boolean-output discipline — but imports none of it: the DETECT env is numeric-only
// and event-shaped, while a group selector is entity-shaped and needs string, numeric,
// and bool facets. One expression language, two envs; never a cross-service import.
package selector

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

// The variable names the selector environment declares — the whole vocabulary a
// selector may reference. cel-go's checker rejects any other identifier, and the
// lowering (lower.go) only accepts `attr[<literal>]` / `<literal> in attr` shapes.
const (
	// VarAttr maps a facet key to the member entity's typed value for it. Unlike
	// DETECT's numeric-only `attr`, a group facet may be string, numeric, or bool
	// (climate="arid", population=1_000_000, coastal=true). Presence is `k in attr`;
	// an entity that has not set the facet is simply not a key (the absence
	// discipline the SQL semi-join preserves — an absent facet makes a leaf false).
	VarAttr = "attr"
	// VarMemberType is the group's member family (device|asset|area|customer),
	// fixed per group in v1 but declared so a future registry-wide selector could
	// branch on it.
	VarMemberType = "memberType"
)

// SchemaVersion is the version of the declared selector shape. It is stamped onto a
// group's SelectorSchema when its selector is compiled, so a future additive field
// (a new declared variable) can be rolled out by bumping this and recompiling. Bump
// on any change to the declarations below.
//
//   - v1: attr (string/numeric/bool facet map), memberType.
const SchemaVersion = 1

var (
	sharedEnv     *cel.Env
	sharedEnvErr  error
	sharedEnvOnce sync.Once
)

// Env returns the process-wide shared CEL environment for the current SchemaVersion.
// One env is built once and reused for every Compile: cel.Env is safe for concurrent
// use and building it is not cheap, so it must not be per-selector. It fails closed —
// a construction error is returned to every caller rather than a half-built env.
func Env() (*cel.Env, error) {
	sharedEnvOnce.Do(func() {
		sharedEnv, sharedEnvErr = cel.NewEnv(
			// attr is map(string, dyn): the checker accepts most comparisons on attr[k]
			// (its element type is dyn), so the real type guard is the lowering's operand
			// rules (lower.go) plus the SQL value_type gate (value predicates), not the
			// checker. This mirrors the deliberately-shallow DETECT typing.
			cel.Variable(VarAttr, cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable(VarMemberType, cel.StringType),
		)
	})
	if sharedEnvErr != nil {
		return nil, fmt.Errorf("build selector environment (schema v%d): %w", SchemaVersion, sharedEnvErr)
	}
	return sharedEnv, nil
}
