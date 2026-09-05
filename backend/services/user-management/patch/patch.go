// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package patch holds the two three-state folds user-management needs and core's
// graphql package deliberately does not provide. Both live here rather than being
// copied into admin and identity because both packages need them, and a second copy
// is how two call sites come to disagree about what "cleared" means.
package patch

import (
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
)

// EmptiableString folds an OptionalString onto a string column that CANNOT SPELL NULL and
// whose EMPTY VALUE IS A LEGITIMATE STATE: absent keeps, a value sets it verbatim, and an
// explicit null writes "".
//
// # 🔴 WHY THIS IS NOT ApplyToRequired, AND WHY CORE HAVING DELETED ApplyToValue IS NOT
// AN ARGUMENT AGAINST IT
//
// core's optional.go removed a general ApplyToValue on the finding that every call site
// it had been written for was a NOT NULL column whose zero value is NOT a state the
// entity may be in — a vocabulary string, a required flag — where folding a null to the
// zero value accepts "clear this required field" and writes a row the create path would
// have refused. ApplyToRequired refuses those, and that is right.
//
// The three columns this serves are the counter-case, and each is one whose EMPTY value
// the create path already writes and the read path already renders. They arrive here by
// two DIFFERENT routes, and an earlier version of this comment gave the wrong one for two
// of the three — it called all three columns NOT NULL, which the golden schema
// contradicts. The distinction is worth keeping straight, because only one of them is
// settled:
//
//   - iam.TenantTier.Color is the genuine case: `color character varying(32)` declared
//     NOT NULL with an empty-string default in the golden schema, where the empty string
//     means "no pill" and is what ValidTierColor accepts alongside every palette token.
//     The column has no NULL to reach, so the empty string IS the clear, and this fold is
//     justified there without qualification.
//   - iam.Identity.FirstName / LastName reach the same fold for a WEAKER reason: the
//     COLUMNS are nullable (`first_name character varying(128)`, no NOT NULL), but the Go
//     model holds a bare `string` (iam/model.go), which cannot represent the null the
//     column would accept. So "" is the only empty this code path can write, and
//     `updateProfile(firstName: "")` has always written it. ApplyToRequired would refuse
//     that as blank, removing a capability rather than converting one.
//
// So an explicit null here is not "clear a required field"; it is the only spelling of an
// empty this model can produce. What is NOT available is a third state: "" and null are
// the same stored value, exactly as they are for a list.
//
// 🔴 THE SECOND CASE HAS AN HONEST FIX AND THIS IS NOT IT. core's deletion note
// prescribes it exactly — "the honest fix is to make the column nullable, not to bring
// this back" — and for these two the column ALREADY IS: only the model has to change, to
// sql.NullString, and no migration is involved. That is a change to a type used across
// this service and is filed separately rather than folded into a partial-update
// conversion. Until it lands, this fold is what the model can express, and the reason
// above is the true one.
func EmptiableString(o dcgraphql.OptionalString, current string) string {
	if !o.Set {
		return current
	}
	if o.Value == nil {
		return ""
	}
	return *o.Value
}

// IntPtr folds an OptionalInt32 — GraphQL's Int is 32-bit by specification — onto a
// nullable *int model column.
//
// The width change is the whole reason it exists. dcgraphql.OptionalInt32.ApplyTo works
// on *int32, every governance override on iam.Tenant is a *int, and writing the
// conversion out at each of the eight call sites is how one of them ends up dropping the
// nil — which for a governance override means "inherit the platform default", never
// unlimited — or coercing it to zero, which the cascade reads as an override that means
// nothing and the enforcing service floors back to the default while reporting the
// tenant as mis-configured.
//
// nil in, nil out: an explicit null REMOVES the override rather than zeroing it.
//
// 🔴 THE ABSENT BRANCH RETURNS current UNTOUCHED, AND THAT IS NOT MERELY AN EARLY EXIT.
// The stored column is a bigint behind a Go `int`; the wire type is a 32-bit GraphQL Int.
// An earlier version converted `current` through int32 before handing it to ApplyTo, so a
// field the caller never mentioned round-tripped its stored value through a NARROWING
// cast — on the one branch whose entire contract is "touch nothing". No value reachable
// through the API today is wide enough to lose anything, because both write doors take a
// GraphQL Int; the point is that a fold meaning "leave it alone" must not be the thing
// that changes it, whatever a future writer puts in the column.
func IntPtr(o dcgraphql.OptionalInt32, current *int) *int {
	if !o.Set {
		return current
	}
	if o.Value == nil {
		return nil
	}
	out := int(*o.Value)
	return &out
}
