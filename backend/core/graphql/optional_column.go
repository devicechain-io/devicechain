// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/sqlnull"
)

// THE COLUMN FOLDS: a partial update folded straight onto the type the model stores.
//
// ApplyTo works on the wire's pointer shape, so a nullable column used to be folded as
// `rdb.NullStrOf(x.ApplyTo(NullStr(row.X)))` — out of the column, through the fold, and
// back in through the create path's normalizer. That round trip ran on the ABSENT branch
// too, whose whole contract is "leave it alone": a stored value with surrounding
// whitespace was trimmed by an update that never named it, and a bigint interval was
// narrowed through int32 by an update that never named it.
//
// These take and return the COLUMN type, so the absent branch hands back exactly what was
// stored — no pointer round trip, no second pass through a normalizer — and the value
// branch applies the rule that column's create path applies (package sqlnull), so there is
// one definition of each rule and the fold and the create path cannot disagree.

// ApplyToNullString folds a String field onto a NULLABLE DESCRIPTIVE-TEXT column.
//
//	absent  -> current, UNTOUCHED (not trimmed: a field the caller did not name is not rewritten)
//	null    -> NULL
//	value   -> sqlnull.Text(value): trimmed, and a blank value clears
//
// NOT FOR SECRETS — see ApplyToNullSecret.
func (o OptionalString) ApplyToNullString(current sql.NullString) sql.NullString {
	if !o.Set {
		return current
	}
	return sqlnull.Text(o.Value)
}

// ApplyToNullSecret folds a String field onto a nullable SECRET column (a device
// credential's value): absent keeps, null clears, and a value is stored EXACTLY AS SENT —
// only "" clears (sqlnull.Secret). A secret is compared byte for byte against what a
// device presents, so any transformation here makes a credential nothing can use.
func (o OptionalString) ApplyToNullSecret(current sql.NullString) sql.NullString {
	if !o.Set {
		return current
	}
	return sqlnull.Secret(o.Value)
}

// ApplyToNullInt64 folds an Int field onto a nullable bigint column. The absent branch
// returns current untouched, so a stored value wider than 32 bits is never narrowed by an
// update that did not mention it. A value only widens.
func (o OptionalInt32) ApplyToNullInt64(current sql.NullInt64) sql.NullInt64 {
	if !o.Set {
		return current
	}
	return sqlnull.Int64FromInt32(o.Value)
}

// ApplyToIntPtr folds an Int field onto a nullable column the model holds as *int (a
// tenant governance override).
//
// nil in, nil out: an explicit null REMOVES the override — "inherit" — rather than zeroing
// it. Coercing it to zero instead is what the cascade reads as an override that means
// nothing, and the enforcing service floors back to the default while reporting the tenant
// as mis-configured.
//
// 🔴 THE ABSENT BRANCH RETURNS current UNTOUCHED, AND THAT IS NOT MERELY AN EARLY EXIT.
// The stored column is a bigint behind a Go `int`; the wire type is a 32-bit GraphQL Int.
// An earlier version converted `current` through int32 before handing it to ApplyTo, so a
// field the caller never mentioned round-tripped its stored value through a NARROWING
// cast — on the one branch whose entire contract is "touch nothing". No value reachable
// through the API today is wide enough to lose anything, because both write doors take a
// GraphQL Int; the point is that a fold meaning "leave it alone" must not be the thing
// that changes it, whatever a future writer puts in the column.
func (o OptionalInt32) ApplyToIntPtr(current *int) *int {
	if !o.Set {
		return current
	}
	if o.Value == nil {
		return nil
	}
	out := int(*o.Value)
	return &out
}
