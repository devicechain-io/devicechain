// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import "testing"

func TestShedClassOfBands(t *testing.T) {
	cases := []struct {
		priority int
		want     ShedClass
	}{
		// Best-effort band (1–19) and out-of-range-low.
		{1, ShedBestEffort},
		{19, ShedBestEffort},
		{0, ShedBestEffort},  // non-positive should never reach here; sheds first if it does
		{-5, ShedBestEffort}, // an out-of-range low value is read as "sheds first", never gold
		// Bronze band (20–49) — includes DefaultShedPriority.
		{20, ShedBronze},
		{DefaultShedPriority, ShedBronze},
		{49, ShedBronze},
		// Silver band (50–79).
		{50, ShedSilver},
		{79, ShedSilver},
		// Gold band (80–100) and out-of-range-high.
		{80, ShedGold},
		{100, ShedGold},
		{500, ShedGold},
	}
	for _, c := range cases {
		if got := ShedClassOf(c.priority); got != c.want {
			t.Errorf("ShedClassOf(%d) = %d, want %d", c.priority, got, c.want)
		}
	}
}

// TestDefaultShedPriorityIsFailSafe pins the fail-safe: the platform default must
// band to a class that IS shed under contention (never gold), so an unclassifiable
// tenant degrades before the premium ones rather than riding through ahead of them.
func TestDefaultShedPriorityIsFailSafe(t *testing.T) {
	class := ShedClassOf(DefaultShedPriority)
	if class == ShedGold {
		t.Fatalf("DefaultShedPriority (%d) bands to gold — an absent priority must never fail open to never-shed", DefaultShedPriority)
	}
	// And it must actually be shed at some level, i.e. not merely below gold but
	// genuinely sheddable — proving the fail-safe leaves the tenant exposed to shedding.
	if ShedFactor(class, MaxShedLevel) >= 1.0 {
		t.Fatalf("DefaultShedPriority class %d is never shed even at the deepest level — not a fail-safe", class)
	}
}

// TestGoldIsNeverShed pins the ADR-063 promise: gold's factor is 1.0 at EVERY level.
// A mutation that sheds gold at any level (the exact failure the whole mechanism must
// not have) fails here.
func TestGoldIsNeverShed(t *testing.T) {
	for level := 0; level <= MaxShedLevel; level++ {
		if f := ShedFactor(ShedGold, level); f != 1.0 {
			t.Errorf("ShedFactor(gold, L%d) = %v, want 1.0 — gold must never be shed", level, f)
		}
	}
	// Out-of-range levels clamp and must still never shed gold.
	if f := ShedFactor(ShedGold, MaxShedLevel+5); f != 1.0 {
		t.Errorf("ShedFactor(gold, over-max) = %v, want 1.0", f)
	}
}

// TestShedLadderStartsEachClass pins which level first sheds each class (ADR-063:
// L1 best-effort, L2 +bronze, L3 +silver). "Sheds" means factor < 1; "not yet"
// means factor == 1.
func TestShedLadderStartsEachClass(t *testing.T) {
	firstShedLevel := map[ShedClass]int{
		ShedBestEffort: 1,
		ShedBronze:     2,
		ShedSilver:     3,
	}
	for class, first := range firstShedLevel {
		for level := 0; level <= MaxShedLevel; level++ {
			f := ShedFactor(class, level)
			shed := f < 1.0
			wantShed := level >= first
			if shed != wantShed {
				t.Errorf("class %d at L%d: shed=%v (factor %v), want shed=%v (first sheds at L%d)",
					class, level, shed, f, wantShed, first)
			}
		}
	}
}

// TestShedLevel0ShedsNothing pins that no class is shed at level 0 (the no-contention
// / manual_floor=0 state — the harness's negative control).
func TestShedLevel0ShedsNothing(t *testing.T) {
	for class := ShedBestEffort; class <= ShedGold; class++ {
		if f := ShedFactor(class, 0); f != 1.0 {
			t.Errorf("ShedFactor(class %d, L0) = %v, want 1.0 — L0 sheds nothing", class, f)
		}
	}
}

// TestShedFactorMonotonicInLevel pins that deepening the level never sheds a class
// LESS (a deeper level is at least as aggressive).
func TestShedFactorMonotonicInLevel(t *testing.T) {
	for class := ShedBestEffort; class <= ShedGold; class++ {
		for level := 1; level <= MaxShedLevel; level++ {
			prev := ShedFactor(class, level-1)
			cur := ShedFactor(class, level)
			if cur > prev {
				t.Errorf("class %d: factor rose from %v (L%d) to %v (L%d) — a deeper level must not shed less",
					class, prev, level-1, cur, level)
			}
		}
	}
}

// TestShedFactorMonotonicInClass pins that at any level a lower class is shed at
// least as hard as a higher one — the "who degrades last" ordering. A typo that let
// bronze survive more than best-effort, or silver more than bronze, fails here.
func TestShedFactorMonotonicInClass(t *testing.T) {
	for level := 0; level <= MaxShedLevel; level++ {
		for class := ShedBestEffort; class < ShedGold; class++ {
			lower := ShedFactor(class, level)
			higher := ShedFactor(class+1, level)
			if lower > higher {
				t.Errorf("L%d: class %d factor %v > class %d factor %v — a lower class must not survive more",
					level, class, lower, class+1, higher)
			}
		}
	}
}

func TestShedLimitsUnchangedAtFactorOne(t *testing.T) {
	l := Limits{MessagesPerSecond: 1000, Burst: 2000}
	got := l.Shed(1.0)
	if got != l {
		t.Errorf("Shed(1.0) = %+v, want unchanged %+v", got, l)
	}
	// A factor above 1 (should not occur, but defensively) also leaves it unchanged
	// rather than amplifying the ceiling.
	if got := l.Shed(1.5); got != l {
		t.Errorf("Shed(1.5) = %+v, want unchanged %+v (never amplify)", got, l)
	}
}

func TestShedLimitsHardDropAtFactorZero(t *testing.T) {
	l := Limits{MessagesPerSecond: 1000, Burst: 2000}
	got := l.Shed(0)
	if got.MessagesPerSecond != 0 || got.Burst != 0 {
		t.Errorf("Shed(0) = %+v, want a hard drop {0,0}", got)
	}
}

// TestShedLimitsThrottleFloorsBurst pins that a throttle (0<factor<1) never rounds
// burst to 0 — that would silently turn a throttle into a hard drop, shedding a
// class as if it were dropped.
func TestShedLimitsThrottleFloorsBurst(t *testing.T) {
	// A tiny base burst with a small factor would round to 0 without the floor.
	l := Limits{MessagesPerSecond: 10, Burst: 2}
	got := l.Shed(0.10)
	if got.Burst < 1 {
		t.Errorf("Shed(0.10) burst = %d, want floored to >= 1 (a throttle must still admit)", got.Burst)
	}
	if got.MessagesPerSecond != 1.0 {
		t.Errorf("Shed(0.10) rate = %v, want 1.0 (10 * 0.10)", got.MessagesPerSecond)
	}
}

func TestShedLimitsScalesProportionally(t *testing.T) {
	l := Limits{MessagesPerSecond: 1000, Burst: 2000}
	got := l.Shed(0.25)
	if got.MessagesPerSecond != 250 {
		t.Errorf("Shed(0.25) rate = %v, want 250", got.MessagesPerSecond)
	}
	if got.Burst != 500 {
		t.Errorf("Shed(0.25) burst = %d, want 500", got.Burst)
	}
}
