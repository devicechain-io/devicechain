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
// The reserve is `reserved = ceil(ceiling × r)`, rounding in favour of the reserve, and the
// limit is what is left. Note the exact r: the configured fraction is first QUANTIZED to
// basis points, so r is `round(reserve × 10000) / 10000` and not the fraction verbatim. A
// reserve of 1/3 against a ceiling of 10000 therefore reserves 3333, where ceil of the
// unquantized product would be 3334. The gap is bounded by half a basis point and is the
// price of exact integer arithmetic; it is called out because "never under-provisioned"
// would otherwise read as an absolute, and it is not one.
//
// Both arguments are treated fail-safe: a non-positive ceiling means the platform ceiling,
// and a reserve outside (0,1) means the platform reserve.
//
// 🔴 ZERO DOES NOT MEAN "NO RESERVE". It means the platform default, exactly as a zero
// ceiling means the platform ceiling (ADR-023: the harmless-looking value must land on the
// bound, not remove it). An operator who wants the reserve to be negligible sets a small
// positive fraction; there is deliberately no value that switches it off, because the
// thing it protects is the platform's ability to deliver at all.
//
// 🔴 THE max(1, …) IS NOT DEFENSIVE PADDING — a zero limit is not a tighter bound, it is
// an outage in which the tenant may enqueue nothing at all, the same zero-inversion the
// ceiling resolver refuses when a tier answers zero, arriving here through the reserve.
//
// What actually reaches it is worth naming, because the obvious answer is wrong: a reserve
// of 1 or more does NOT, since the fail-safe above has already replaced it with the
// default. The live triggers are a tiny ceiling — restricted(1, 0.2) and restricted(2, 0.8)
// both clamp to 1, since `reserved` is at least 1 — and a reserve so close to 1 that its
// basis points round to 10000, e.g. restricted(10000, 0.99996). Neither is reachable
// through the service's own config (which floors the ceiling at 100 and caps the reserve at
// half), so this is the guard for a caller that does not go through that config.
//
// 🔴 THE ARITHMETIC IS INTEGER ON PURPOSE. The obvious spelling, `floor(ceiling × (1 −
// reserve))`, LOSES AN INTEGER at ordinary values, because `1 − reserve` is not exact in
// binary. The pinned example: reserve 0.8, ceiling 10000 yields 1999 where the answer is
// 2000. No count of "how often" is given here on purpose — it depends entirely on which
// (ceiling, reserve) pairs you sweep, so any number quoted would be a fact about the
// author's grid rather than about the arithmetic. What is stable is that the error exists
// and is silent. Basis points keep it exact, and int64 keeps the product from overflowing
// a 32-bit int (a ceiling above ~214k would wrap, which is a plausible-looking limit
// rather than a refused one — the failure mode a narrowing conversion always has).
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
