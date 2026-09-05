// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔴 THE EXHAUSTIVENESS CHECK OVER THIS SERVICE'S UPDATE SURFACE.
//
// The guard itself is core's putest.AssertEveryUpdateTakesADedicatedRequest — it
// enumerates *Api's own Update* methods and asks three structural things of each: that the
// request type is REGISTERED with partialUpdateFamilies() so something drives its three
// states against a real database; that it carries no Token field; and that every exported
// field carries the three states. Its header says which mutants walked past the name-only
// version it replaced, and why none of the three is a check on spelling.
//
// What is local is what only this service can say: which updates have NOT been converted,
// and how many Update* methods reflection must find before the walk is believable.
//
// # 🔴 RenameNotificationChannel IS NOT IN NotAnEntityUpdate, AND MUST NOT BE
//
// That map keys on the names the guard WALKS, and the walk is every method whose name
// begins with "Update". A rename does not, so it is never in the walked set and never
// needs excusing — naming it there would be a stale entry, which the guard fails on for
// the same reason it fails on a stale exemption.
//
// The rename is genuinely outside this rule rather than merely outside its name filter: it
// takes (ctx, token, newToken), three strings and no input object, and the three states of
// a partial update have no meaning for a single required argument. Its whole contract — a
// blank token refused, the current token an idempotent no-op, a taken token refused by
// name, the delivery secret surviving — is driven directly in api_token_argument_test.go
// and api_test.go, which is where the mutation's own name will send a reader looking for it.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Api]{
		Families: partialUpdateFamilies(),

		// No exemptions. Both of this service's updates take a dedicated *UpdateRequest,
		// so the residual a reader is asked to count is zero — and an entry added here
		// later would have to name a method that exists, since an exemption matching
		// nothing fails as loudly as one naming the wrong type.
		Exempt: nil,

		// Nothing to exclude either: both walked methods resolve to an input object, so
		// neither needs the "this rule does not govern it" escape.
		NotAnEntityUpdate: nil,

		// The anti-vacuity floor, and it counts EVERY Update* method rather than the ones
		// that matched a shape — so a method the guard cannot classify raises the count it
		// has to clear instead of vanishing from it. Reflection over a renamed or embedded
		// receiver could find nothing at all, and a loop over nothing reports success.
		// This service has exactly two: UpdateNotificationChannel and
		// UpdateNotificationPolicy. That the floor is the real total and not a number
		// picked low enough to pass was checked by raising it to 3 and watching it fail.
		MinUpdateMethods: 2,
	})
}
