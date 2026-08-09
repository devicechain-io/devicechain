// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package predicate

import (
	"fmt"
	"reflect"

	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// geoCelType is the opaque CEL type of the `geo` variable. It is opaque on purpose: a rule can
// do exactly one thing with it — call the containment function below — and cannot read a
// position out of it, compare it, or convert it. Whatever the runtime binds into it is therefore
// unreachable except through that one door.
var geoCelType = cel.OpaqueType("dc.detect.Geo")

// geoValue is the per-evaluation binding behind `geo`: ONE event's position and THE ONE fence set
// its stamped fence-set version names.
//
// 🔴 THIS IS WHY FENCES ARRIVE BY ACTIVATION AND NOT BY CLOSURE. The CEL environment is a
// process-wide singleton built once under sync.Once, so anything captured in a function's Go
// closure is shared by every tenant's every rule for the life of the process — a fence set
// reached that way would be a cross-tenant channel by construction, and keeping it correct would
// mean a lock and a lookup the closure has no tenant to key on. The activation is per evaluation
// and is built by the runtime from ONE resolved event, so the only fence set reachable from a
// rule is the one the caller resolved for (that event's tenant, that event's stamped version).
// Tenant isolation here is structural, not a check that could be forgotten.
type geoValue struct {
	// position is the event's reported position; nil when the event carries none.
	position *geofence.Position
	// fences is the frozen fence set of the event's stamped version. NIL MEANS UNRESOLVABLE, not
	// empty — a tenant with no fences resolves to a known-empty set (geofence.EmptyFenceSet), and
	// the two answer differently on purpose.
	fences *geofence.FenceSet
}

// inFence answers containment for one fence token. It never returns a bare false for a question
// it could not answer: no position, no fence set, and no such fence are each an error, which the
// runtime turns into a SKIPPED sample rather than a false leaf (a false would cancel a Duration
// rule's in-flight hold and would read as a healthy, quiet rule). See geofence's sentinels.
func (g geoValue) inFence(token string) ref.Val {
	if g.position == nil {
		return types.WrapErr(fmt.Errorf("%w: inFence(%q)", geofence.ErrNoPosition, token))
	}
	in, err := g.fences.Contains(token, *g.position)
	if err != nil {
		return types.WrapErr(err)
	}
	return types.Bool(in)
}

// ConvertToNative refuses every native conversion: `geo` is opaque, so there is nothing to hand
// back to Go.
func (g geoValue) ConvertToNative(typeDesc reflect.Type) (any, error) {
	return nil, fmt.Errorf("%s cannot be converted to %v", geoCelType.TypeName(), typeDesc)
}

// ConvertToType supports only `type(geo)`; every other conversion is an error rather than a
// silent value of another shape.
func (g geoValue) ConvertToType(t ref.Type) ref.Val {
	if t == types.TypeType {
		return geoCelType
	}
	return types.NewErr("type conversion from %s to %s is not supported", geoCelType.TypeName(), t.TypeName())
}

// Equal is always false: two events' geo bindings are not comparable, and returning an error
// here would make a stray `==` fail the whole leaf rather than simply not match.
func (g geoValue) Equal(other ref.Val) ref.Val { return types.False }

// Type is the opaque CEL type.
func (g geoValue) Type() ref.Type { return geoCelType }

// Value returns the receiver so the function binding can recover it.
func (g geoValue) Value() any { return g }

// inFenceOverload declares the ONE containment function of the DETECT environment.
//
// 🔴 ONE FUNCTION, N GEOMETRY KINDS. `geo.inFence("yard")` is the whole surface. The fence's
// geometry kind — 2D polygon today, polygon-plus-altitude-band or voxel set later — is resolved
// inside the fence set (geofence's `containment` dispatch) and never appears in the rule
// language. Adding a kind therefore adds no function, changes no published rule, and does not
// re-open this environment. The alternative, a second `inVoxelRegion()` beside this one, would
// make UPGRADING A FENCE FROM 2D TO 2.5D A RULE MIGRATION rather than a fence edit.
func inFenceOverload() cel.EnvOption {
	return cel.Function(FuncInFence,
		cel.MemberOverload(FuncInFence+"_geo_string",
			[]*cel.Type{geoCelType, cel.StringType}, cel.BoolType,
			cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
				g, ok := lhs.Value().(geoValue)
				if !ok {
					// Unreachable: the type checker admits only the declared `geo` variable here.
					return types.NewErr("inFence was applied to a %T rather than the event's geo binding", lhs.Value())
				}
				token, ok := rhs.Value().(string)
				if !ok {
					return types.NewErr("inFence expects a geofence token string, got %T", rhs.Value())
				}
				return g.inFence(token)
			})))
}
