// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import "math"

// DefaultDeliveryMachineryReserve is the fraction of a tenant's undelivered-command
// ceiling kept for the platform's own command delivery — the share a fleet write
// cannot consume.
//
// The problem it solves is not a tenant exceeding its ceiling; the ceiling already
// handles that. It is that a single legitimate fleet write can consume the WHOLE
// ceiling at once and, from that moment, every automated send-command the platform
// tries to enqueue for that tenant is refused. Without a reserve, "reboot every pump"
// silently disables the tenant's alarm-driven automation until the backlog drains.
//
// 🔴 IT IS A DELIVERY-MACHINERY RESERVE, NOT AN "AUTOMATION" RESERVE, and the name
// matters because it describes who is actually protected. The split is by TOKEN CLASS:
// only the platform's own service-token callers draw on the reserve. A tenant's own
// integration automating against the API holds an access token like a human does, and
// is capped with the humans. Calling this an automation reserve would promise tenants
// something it does not give them, and they would ask for service credentials to get it.
const DefaultDeliveryMachineryReserve = 0.20

// reserveBasisPointsScale converts the configured fraction to basis points so the
// split can be computed in integer arithmetic. See RestrictedCommandLimit.
const reserveBasisPointsScale = 10000

// RestrictedCommandLimit is how many undelivered commands a RESTRICTED caller (anything
// but a platform service token) may hold, given the ceiling in force and the reserve
// fraction. A machinery caller is bounded by the ceiling itself, not by this.
//
// The reserve is defined as `reserved = ceil(ceiling × reserve)` — rounding in favour of
// the reserve, so the protected share is never under-provisioned — and the limit is what
// is left. Both arguments are treated fail-safe: a non-positive ceiling means the platform
// ceiling, and a reserve outside (0,1) means the platform reserve.
//
// 🔴 ZERO DOES NOT MEAN "NO RESERVE". It means the platform default, exactly as a zero
// ceiling means the platform ceiling (ADR-023: the harmless-looking value must land on the
// bound, not remove it). An operator who wants the reserve to be negligible sets a small
// positive fraction; there is deliberately no value that switches it off, because the
// thing it protects is the platform's ability to deliver at all.
//
// 🔴 THE max(1, …) IS NOT DEFENSIVE PADDING. A reserve at or above 1 would compute a
// limit of zero, and a zero limit is not a tighter bound — it is an outage in which the
// tenant may enqueue nothing at all. That is the same zero-inversion the ceiling resolver
// refuses when a tier answers zero, arriving here through the reserve instead.
//
// 🔴 THE ARITHMETIC IS INTEGER ON PURPOSE. The obvious spelling, `floor(ceiling × (1 −
// reserve))`, LOSES AN INTEGER at ordinary values, because `1 − reserve` is not exact in
// binary: at reserve 0.8 and ceiling 10000 it yields 1999 where the answer is 2000, and it
// is wrong for 34 of the 180 (ceiling, reserve) pairs it was measured over. Basis points
// keep it exact, and int64 keeps the product from overflowing a 32-bit int (a ceiling above
// ~214k would wrap, which is a plausible-looking limit rather than a refused one — the
// failure mode a narrowing conversion always has).
//
// 🔴 That defect only appears with RUNTIME values, which is what a configured reserve
// always is. Written with Go constants the subtraction is folded in arbitrary precision
// and the naive form gives the right answer — so a reproduction attempt using constants
// disproves nothing. Both halves are pinned in the test.
func RestrictedCommandLimit(ceiling int, reserve float64) int {
	if ceiling <= 0 {
		ceiling = DefaultHeldCommandCeiling
	}
	if !(reserve > 0 && reserve < 1) { // also rejects NaN, which no comparison would
		reserve = DefaultDeliveryMachineryReserve
	}

	basisPoints := int64(math.Round(reserve * reserveBasisPointsScale))
	if basisPoints < 1 {
		// A fraction so small it rounds to nothing still reserves something; "a reserve
		// is configured" and "no reserve" must not be the same state.
		basisPoints = 1
	}
	product := int64(ceiling) * basisPoints
	reserved := (product + reserveBasisPointsScale - 1) / reserveBasisPointsScale // ceil
	if reserved < 1 {
		reserved = 1
	}

	limit := int64(ceiling) - reserved
	if limit < 1 {
		return 1
	}
	return int(limit)
}
