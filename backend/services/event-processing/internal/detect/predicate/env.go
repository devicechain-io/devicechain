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
	"github.com/google/cel-go/ext"
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
	// projection: the loop owns a flattened per-device view (runtime.DeviceAttributeView),
	// resolves it once per message, and binds it onto every sample's Input in the fan-out.
	//
	// WHAT AN EMPTY attr MEANS, AND WHY IT IS STILL THE INTERESTING CASE. attr is empty for any
	// device that has set no numeric SERVER/SHARED attribute, and on the scaffold path where no
	// view is wired — so "empty" is a normal steady state, not a pre-go-live one. Because the
	// STRUCTURED generator only ever emits a POSITIVE presence guard (`"k" in attr && …`), a
	// structured dynamic rule cleanly does NOT fire against an empty map: it cannot mis-fire on
	// absent state. A RAW-CEL leaf is not bound by that. An author who writes a NEGATED presence
	// (`!("k" in attr) && …`) gets an always-true guard for exactly the devices that have not set
	// the attribute, so the rule fires across the part of the fleet that has no bound — and stops
	// firing per-device as each one's attribute arrives. That is live behaviour today, not a
	// transitional window that closes. It is the same "raw-CEL author owns totality" contract the
	// Duration trap documents (compile.go): the structured path is the safe one; a raw leaf
	// referencing attr owns its own correctness on devices where the key is absent.
	VarAttr = "attr"
	// VarGeo is the event's GEO BINDING: an OPAQUE value carrying the reporting device's
	// position and the frozen fence set of the fence-set version stamped on that event (ADR-078).
	// It has exactly one operation — the containment function below — and no readable structure:
	// a rule cannot get a latitude, a fence list, or a fence's geometry out of it. It is bound per
	// evaluation from ONE resolved event, which is what confines a rule to its own tenant's
	// fences (see geoValue).
	VarGeo = "geo"
)

// FuncInFence is the SOLE geofence containment function: `geo.inFence("<fence token>")`, true
// when the event's position is inside the named fence of the event's own frozen fence set.
// One function for every geometry kind — see inFenceOverload for why that is the load-bearing
// decision and not a convenience.
const FuncInFence = "inFence"

// SchemaVersion is the version of the declared event shape. Nothing consumes it yet: it is
// the seam a future compiled-program cache would key on (recompiling a rule when the schema
// version it was checked against changes), and the marker a future additive field (a new
// declared variable) bumps. Bump on any change to the declarations below.
//
//   - v1: device, anchors, occurred, m.
//   - v2: + attr (device-attribute map, dynamic thresholds, ADR-051 slice 4c-3b).
//   - v3: + geo and its inFence containment function (geofences, ADR-078).
//
// v3 IS A BUMP AND THE REASONING IS WORTH KEEPING, because "the addition is purely additive, so
// nothing needs to change" is the tempting and wrong conclusion. Additive-ness is about existing
// rules — every rule that compiled under v2 compiles byte-identically under v3, because nothing
// was removed, renamed or retyped. The version is about the ENVIRONMENT a compiled artifact was
// CHECKED AGAINST, and that is now a strictly larger vocabulary: a rule written today can name
// `geo`, and that rule does NOT compile under v2. A cache keyed on a stale version would serve a
// program built against an environment the source no longer type-checks in — which is precisely
// the confusion the field exists to prevent. Compare ext.Bindings(), which was NOT a bump: it
// added a scoping MACRO with no new identifier, so the declared shape was unchanged.
const SchemaVersion = 3

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
			// The `cel.bind(name, init, expr)` scoping macro (ADR-053 slice 9a-2): the ONLY surface
			// a compute node adds. It lets the canvas compiler fold a named compute expression into a
			// raw-CEL leaf as a real scoped binding rather than by text interpolation (injection-safe;
			// the composed result is re-gated here). It is purely additive — a structured leaf or a
			// hand-typed raw-CEL leaf that never writes `cel.bind(...)` compiles byte-identically, so
			// the live rule set is unaffected. No new data access or side-effecting function enters.
			ext.Bindings(),
			// The geofence surface (ADR-078): one opaque variable and one function on it.
			//
			// WHAT BECOMES REACHABLE. Exactly one new fact: for the event being evaluated, whether
			// its reported position lies inside a fence named by a token the rule spells out. That
			// is it.
			//
			// WHAT DOES NOT. `geo` is an OPAQUE type, so a rule cannot read the position out of it,
			// cannot enumerate the tenant's fences, cannot read a fence's geometry, cannot compare
			// two geo values, and cannot convert one to anything. There is no query, no list, no
			// wildcard: a fence is reachable only by naming its exact token, and only through
			// inFence. No I/O, no clock, no state, no mutation enters the environment — inFence is a
			// pure function of the values already bound into the activation.
			//
			// WHY IT CANNOT REACH ANOTHER TENANT'S FENCES. It has nothing to reach WITH. The
			// function's Go binding closes over NOTHING — no view, no store, no map of tenants — and
			// it could not usefully close over anything, because this env is a process-wide
			// singleton built once for every tenant. The only fence set in play is the one hanging
			// off the activation value, and the runtime builds that per event from the pair
			// (the event's tenant, the fence-set version stamped on that event). A rule that could
			// see across tenants would require the fan-out to hand it the wrong set, which is a
			// caller bug in one place rather than an environment-wide capability — the same shape as
			// `attr`, which likewise carries only the source device's own values.
			//
			// AND WHY IT IS NOT A LIVE READ. inFence answers against the FROZEN set the event was
			// stamped with, never the fences that exist now, so the determinism boundary does not
			// move: replaying an event a week later produces the same answer it produced live.
			cel.Variable(VarGeo, geoCelType),
			inFenceOverload(),
		)
	})
	if sharedEnvErr != nil {
		return nil, fmt.Errorf("build DETECT predicate environment (schema v%d): %w", SchemaVersion, sharedEnvErr)
	}
	return sharedEnv, nil
}
