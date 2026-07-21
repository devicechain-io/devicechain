// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import "math"

// ADR-063 preferential shedding. A tenant carries a shed PRIORITY — a stored int
// 1–100 (ADR-063 decision 1) resolved through the same tenant→tier→platform cascade
// the ADR-023 ceilings use (see Tenant.EffectiveShedPriority in user-management),
// but as a STANDALONE scalar, never a rate+burst Dimension. This file turns that
// priority into the two pure functions the shedding service (event-sources) needs:
// which SHED CLASS a priority falls in, and what FACTOR to apply to that class's
// effective ingest ceiling at a given contention level.
//
// The whole mechanism acts only at admission (event-sources lowers the affected
// class's effective ADR-023 ceiling, and the existing limiter sheds the overflow
// with a 429). It never reorders an accepted stream, so replay stays deterministic
// (ADR-063 invariant). Gold is never shed — that is the promise, and it is encoded
// as gold's factor being 1.0 at every level (asserted by TestGoldIsNeverShed).

// ShedClass is the named tier a shed priority falls in. The bands are ADR-063's
// (`80–100 gold · 50–79 silver · 20–49 bronze · 1–19 best-effort`). The vocabulary
// matches the ADR-065 tier entity's, but this is a shed-behavior classification
// derived from the resolved priority int, not the tier entity itself.
type ShedClass int

const (
	// ShedBestEffort (priority 1–19) is shed first and hardest — the lowest
	// contention preference.
	ShedBestEffort ShedClass = iota
	// ShedBronze (priority 20–49) is the platform fail-safe default class (see
	// DefaultShedPriority): the second-lowest, never the top, mirroring ADR-023's
	// "missing → a real limit, never unlimited" rule applied to preference.
	ShedBronze
	// ShedSilver (priority 50–79) is shed only at the deepest level.
	ShedSilver
	// ShedGold (priority 80–100) is NEVER shed — the ADR-063 promise.
	ShedGold
)

// MaxShedLevel is the deepest contention level (ADR-063 L ∈ {0..3}). L0 sheds
// nothing; L1 sheds best-effort; L2 adds bronze; L3 adds silver. gold is never shed.
const MaxShedLevel = 3

// String names the shed class for a BOUNDED metric label (ADR-063 P4 — a tenant label
// is an unbounded, attacker-influenceable cardinality vector, so a shed is attributed
// by its class, of which there are exactly four). Stable strings: they are a metric
// dimension, so renaming one breaks an operator's dashboards.
func (c ShedClass) String() string {
	switch c {
	case ShedBestEffort:
		return "best-effort"
	case ShedBronze:
		return "bronze"
	case ShedSilver:
		return "silver"
	case ShedGold:
		return "gold"
	default:
		return "unknown"
	}
}

// DefaultShedPriority is the platform fail-safe priority for a tenant with no
// priority resolved from its own override or its tier — a bronze-band value, the
// second-lowest class. It is deliberately NOT a gold value: an unclassifiable
// tenant must degrade before the premium ones, never ride through ahead of them,
// exactly as an absent ADR-023 override falls to a real ceiling and never to
// unlimited. Callers resolving a missing priority substitute this before banding.
const DefaultShedPriority = 30

// ShedClassOf bands a resolved shed priority (1–100) into its ShedClass. A value
// below the bronze floor (including a non-positive one that should never reach here,
// since the write path rejects it) bands to best-effort — the safe reading of an
// out-of-range priority is "sheds first", never "rides through". A value at or above
// the gold floor bands to gold regardless of how far above 100 it strays.
func ShedClassOf(priority int) ShedClass {
	switch {
	case priority >= 80:
		return ShedGold
	case priority >= 50:
		return ShedSilver
	case priority >= 20:
		return ShedBronze
	default:
		return ShedBestEffort
	}
}

// shedFactorTable is the GA default shed-factor ladder: shedFactorTable[class][level]
// is the fraction of a class's effective ADR-023 ceiling that survives at that
// contention level. It encodes three invariants (all pinned by tests):
//
//   - Gold is 1.0 at every level (never shed — the promise).
//   - Within a class, a deeper level never sheds LESS (monotonic non-increasing in level).
//   - At any level, a lower class never sheds LESS than a higher one (monotonic in class).
//
// The ladder is throttle-then-drop: a class first throttles when its level is reached
// (best-effort at L1, bronze at L2, silver at L3) and reaches a hard drop (0.0) at the
// deepest level it has been shedding through. The exact fractions are GA defaults and
// are exec-spec tuning (ADR-063 pushes the numbers to the spec); the CONTRACT the gate
// asserts is only "factor < 1 ⇒ the class sheds" and "gold factor == 1.0 ⇒ zero loss".
var shedFactorTable = [4][MaxShedLevel + 1]float64{
	//        L0    L1    L2    L3
	ShedBestEffort: {1.0, 0.25, 0.10, 0.0},
	ShedBronze:     {1.0, 1.00, 0.25, 0.0},
	ShedSilver:     {1.0, 1.00, 1.00, 0.25},
	ShedGold:       {1.0, 1.00, 1.00, 1.0},
}

// ShedFactor returns the fraction of class's effective ingest ceiling that survives
// at contention level. level is clamped to [0, MaxShedLevel] defensively (the config
// path already range-validates it); an unrecognized class is treated as bronze, the
// platform-default class, so a future class added without a table row degrades like
// the fail-safe rather than silently riding through as gold.
func ShedFactor(class ShedClass, level int) float64 {
	if level <= 0 {
		return 1.0
	}
	if level > MaxShedLevel {
		level = MaxShedLevel
	}
	if class < ShedBestEffort || class > ShedGold {
		class = ShedBronze
	}
	return shedFactorTable[class][level]
}

// Shed returns l scaled by factor — the effective ceiling after ADR-063 shedding.
// A factor of 1.0 returns l unchanged (the gold / no-contention path, and the only
// path that must be bit-for-bit identical to un-shed governance). A factor of 0
// yields a ceiling that admits nothing (rate 0, burst 0 — the hard drop). For any
// factor in (0,1) the burst floors to 1 rather than rounding to 0: a throttle must
// still admit the occasional message, and a burst that rounded to 0 would silently
// become a hard drop, shedding a throttled class as if it were dropped.
func (l Limits) Shed(factor float64) Limits {
	if factor >= 1.0 {
		return l
	}
	if factor <= 0 {
		return Limits{MessagesPerSecond: 0, Burst: 0}
	}
	burst := int(math.Round(float64(l.Burst) * factor))
	if burst < 1 {
		burst = 1
	}
	return Limits{MessagesPerSecond: l.MessagesPerSecond * factor, Burst: burst}
}
