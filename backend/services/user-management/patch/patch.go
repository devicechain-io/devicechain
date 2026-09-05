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

// EmptiableString folds an OptionalString onto a NOT NULL string column WHOSE EMPTY
// VALUE IS A LEGITIMATE STATE: absent keeps, a value sets it verbatim, and an explicit
// null writes "".
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
// the create path already writes and the read path already renders:
//
//   - iam.Identity.FirstName / LastName — a person may have no recorded first name, and
//     `updateProfile(firstName: "")` has always cleared one. ApplyToRequired would refuse
//     that as blank, which removes a capability rather than converting one.
//   - iam.TenantTier.Color — `not null; default ''`, where "" means "no pill" and is what
//     ValidTierColor accepts alongside every palette token.
//
// So an explicit null here is not "clear a required field"; it is the only spelling of a
// value the column genuinely holds. What is NOT available is a third state: "" and null
// are the same stored value, exactly as they are for a list.
//
// If any of those columns ever becomes nullable, this stops being the right fold for it
// and OptionalString.ApplyTo becomes the right one — the honest fix then is the column,
// not this.
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
func IntPtr(o dcgraphql.OptionalInt32, current *int) *int {
	var currentWide *int32
	if current != nil {
		v := int32(*current)
		currentWide = &v
	}
	applied := o.ApplyTo(currentWide)
	if applied == nil {
		return nil
	}
	out := int(*applied)
	return &out
}
