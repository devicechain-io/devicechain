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
// enumerates *Api's own Update* methods and asks three structural things of each: that
// the request type is REGISTERED with partialUpdateFamilies() so something drives its
// three states against a real database; that it carries no Token field; and that every
// exported field carries the three states. Its header says which mutants walked past the
// name-only version it replaced, and why none of the three is a check on spelling.
//
// 🔴 IT FINDS THE INPUT BY SHAPE, WHICH IS WHY IT CAN SEE THIS SERVICE AT ALL.
// UpdateDashboard takes a FIFTH parameter — `expectedUpdatedAt *string`, the
// optimistic-concurrency precondition — and the first version of that guard located the
// input at "parameter 3 of exactly 4" and skipped anything else in silence. This service
// has exactly one update, so the skip would have left `seen` at zero and the guard would
// have failed its own anti-vacuity floor with a message blaming reflection: certifying
// nothing, and saying so in a way that reads as the guard being broken rather than the
// service being uncovered.
//
// What is local is what only this service can say: how many Update* methods reflection
// must find before the walk is believable, and — for the residual to be COUNTABLE rather
// than inferred — that there are no unconverted updates and no non-entity ones.
func TestEveryUpdateTakesADedicatedUpdateRequest(t *testing.T) {
	putest.AssertEveryUpdateTakesADedicatedRequest(t, putest.UpdateSurface[*Api]{
		Families: partialUpdateFamilies(),

		// No exemptions and nothing outside the rule. dashboard-management serves ONE
		// update mutation and it is converted, so both maps are deliberately absent
		// rather than empty-for-symmetry: an entry in either is a residual a reader is
		// asked to count, and there is none to count here.

		// The anti-vacuity floor. Reflection over a renamed or embedded receiver could
		// find nothing at all, and a loop over nothing reports success. One is the whole
		// surface: UpdateDashboard. Publish and rollback are not Update* methods, and
		// neither takes an input object to convert.
		MinUpdateMethods: 1,
	})
}
