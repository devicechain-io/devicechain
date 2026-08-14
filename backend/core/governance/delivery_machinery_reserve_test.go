// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"math"
	"testing"
)

// The shipped defaults, spelled out: at the platform ceiling a restricted caller gets
// 8,000 and the delivery machinery keeps 2,000. These are the numbers quoted in the
// refusal message and the docs, so they are asserted rather than derived.
func TestRestrictedCommandLimitAtThePlatformDefaults(t *testing.T) {
	if got := RestrictedCommandLimit(DefaultHeldCommandCeiling, DefaultDeliveryMachineryReserve); got != 8000 {
		t.Fatalf("RestrictedCommandLimit(10000, 0.20) = %d, want 8000", got)
	}
}

// Every fail-safe input lands on a real limit, and none of them removes the reserve.
//
// 🔴 The zero cases are the point. A zero ceiling or a zero reserve is the
// harmless-looking value, and if either read as "unbounded" or "no reserve" the guard
// would evaporate exactly when whatever carries the value is unreachable.
func TestRestrictedCommandLimitFailsSafe(t *testing.T) {
	platform := RestrictedCommandLimit(DefaultHeldCommandCeiling, DefaultDeliveryMachineryReserve)

	for _, tc := range []struct {
		name    string
		ceiling int
		reserve float64
		want    int
	}{
		{"zero ceiling means the platform ceiling", 0, DefaultDeliveryMachineryReserve, platform},
		{"negative ceiling means the platform ceiling", -5, DefaultDeliveryMachineryReserve, platform},
		{"zero reserve means the platform reserve, NOT no reserve", DefaultHeldCommandCeiling, 0, platform},
		{"negative reserve means the platform reserve", DefaultHeldCommandCeiling, -0.5, platform},
		{"a reserve of 1 would be an outage, so it means the platform reserve", DefaultHeldCommandCeiling, 1, platform},
		{"a reserve above 1 likewise", DefaultHeldCommandCeiling, 4.2, platform},
		{"NaN means the platform reserve", DefaultHeldCommandCeiling, math.NaN(), platform},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RestrictedCommandLimit(tc.ceiling, tc.reserve); got != tc.want {
				t.Fatalf("RestrictedCommandLimit(%d, %v) = %d, want %d", tc.ceiling, tc.reserve, got, tc.want)
			}
		})
	}
}

// The counterweight to the fail-safe table: a configured reserve must actually be
// honoured. Without this, a function that ignored its arguments and always returned the
// platform answer would pass every case above.
func TestRestrictedCommandLimitHonoursWhatItIsGiven(t *testing.T) {
	for _, tc := range []struct {
		ceiling int
		reserve float64
		want    int
	}{
		{10000, 0.10, 9000},
		{10000, 0.25, 7500},
		{1000, 0.20, 800},
		{100, 0.20, 80},
		// ceil() in favour of the reserve: 333 × 0.2 = 66.6 ⇒ 67 reserved, 266 left.
		{333, 0.20, 266},
	} {
		if got := RestrictedCommandLimit(tc.ceiling, tc.reserve); got != tc.want {
			t.Errorf("RestrictedCommandLimit(%d, %v) = %d, want %d", tc.ceiling, tc.reserve, got, tc.want)
		}
	}
}

// The limit never reaches zero, however the inputs conspire — a zero limit is an outage
// (the tenant may enqueue nothing), not a tighter bound.
func TestRestrictedCommandLimitNeverReachesZero(t *testing.T) {
	for _, ceiling := range []int{1, 2, 3, 10} {
		for _, reserve := range []float64{0.99, 0.9, 0.5, 0.0001} {
			if got := RestrictedCommandLimit(ceiling, reserve); got < 1 {
				t.Errorf("RestrictedCommandLimit(%d, %v) = %d, want at least 1", ceiling, reserve, got)
			}
		}
	}
}

// naiveFloorLimit is the obvious spelling of the split, kept here only so the trap it
// carries can be demonstrated. Taking its inputs as parameters is load-bearing — see the
// test below.
func naiveFloorLimit(ceiling int, reserve float64) int {
	return int(math.Floor(float64(ceiling) * (1 - reserve)))
}

// 🔴 The float trap this function was written in integers to avoid, pinned so nobody
// "simplifies" it back. `floor(ceiling × (1 − reserve))` is WRONG at ordinary values,
// because `1 − reserve` is not exact in binary.
//
// 🔴🔴 AND THE OBVIOUS WAY TO WRITE THIS TEST DOES NOT REPRODUCE IT. Spelled with
// constants — `const ceiling, reserve = 10000, 0.8` — Go evaluates `1 - reserve` in
// arbitrary precision at compile time, gets exactly 0.2, and the naive form returns the
// RIGHT answer. The bug needs runtime values, which is what a configured reserve always
// is. The first version of this test asserted the trap using constants, failed, and was
// nearly "corrected" into agreeing that no trap exists.
func TestTheObviousFloatSpellingWouldLoseAnInteger(t *testing.T) {
	ceiling, reserve := 10000, 0.8 // variables, NOT constants — see above

	naive := naiveFloorLimit(ceiling, reserve)
	if naive != 1999 {
		t.Fatalf("the naive form now yields %d; it yielded 1999 when this was written — "+
			"if Go's arithmetic changed, re-measure before trusting the comment", naive)
	}
	if got := RestrictedCommandLimit(ceiling, reserve); got != 2000 {
		t.Fatalf("RestrictedCommandLimit(%d, %v) = %d, want 2000 (the naive form gives %d)",
			ceiling, reserve, got, naive)
	}

	// The control that makes the point about constant folding measurable rather than
	// merely asserted in the comment above.
	const cCeiling, cReserve = 10000, 0.8
	if folded := int(math.Floor(float64(cCeiling) * (1 - cReserve))); folded != 2000 {
		t.Fatalf("constant-folded naive form = %d, want 2000; the claim that constants hide "+
			"this trap no longer holds", folded)
	}
}

// The reserve is never under-provisioned: machinery always keeps at least its fraction,
// across a sweep of ceilings and fractions. This is the property the ceil() rounding
// direction exists for, asserted over a range rather than at one point.
func TestTheReserveIsNeverUnderProvisioned(t *testing.T) {
	for _, ceiling := range []int{100, 128, 333, 1000, 10000, 65536, 100000} {
		for _, reserve := range []float64{0.05, 0.1, 0.15, 0.2, 0.25, 0.5, 0.8, 0.9} {
			limit := RestrictedCommandLimit(ceiling, reserve)
			reserved := ceiling - limit
			if want := float64(ceiling) * reserve; float64(reserved) < want-1e-9 {
				t.Errorf("ceiling=%d reserve=%v reserved %d, want at least %v",
					ceiling, reserve, reserved, want)
			}
			if limit >= ceiling {
				t.Errorf("ceiling=%d reserve=%v left the restricted limit at %d, reserving nothing",
					ceiling, reserve, limit)
			}
		}
	}
}
