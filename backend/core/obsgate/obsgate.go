// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package obsgate is an EXPERIMENT: authority that narrows by itself when the data behind it
// goes missing.
//
// An automation, an agent or an operator is granted some authority over a scope of assets
// (dim these lights, close these valves). This package ties how much of that authority may be
// USED to how well the scope is currently OBSERVED: how fresh the latest readings are, how
// much of the scope they cover, and how much the source of each reading can be trusted. When
// observation degrades, authority for new actions narrows automatically, from FULL to REDUCED
// to OBSERVE_ONLY, and an action on an asset that is itself not observed is refused even when
// the scope as a whole is well observed. No data is unknown; unknown never grants.
//
// It is a pure library: every time is an argument and nothing is stored, so a decision can be
// replayed. The obssim subpackage drives it through simulated fleets; nothing in a service
// calls it yet. What REDUCED permits (reversible actions only, a smaller step, a slower rate)
// belongs to the caller; this package only says which level the evidence supports.
//
// # What it narrows, and what it never touches
//
// It narrows NEW authority only. It does not revoke an action already running, and it does not
// stop local operation that was granted before observation degraded (a node-held schedule keeps
// running when the backhaul is cut). Failing closed everywhere would turn a monitoring outage
// into a city outage; failing closed on new authority does not.
//
// # The rules
//
//   - A target meets a level's requirement when it has at least one reading at or above the
//     level's minimum provenance, no older than the level's maximum age.
//   - A reading stamped further in the future than the allowed clock skew counts for nothing.
//     Without that rule a device whose clock runs ahead, or a relay that re-stamps a cached
//     value, would read as fresh for as long as its clock stays ahead.
//   - The scope's level is the highest level whose coverage (the share of the scope's targets
//     meeting that level's requirement) reaches the level's minimum coverage, capped by the
//     level granted. An empty scope is OBSERVE_ONLY.
//   - The FULL requirement must be at least as strict as REDUCED in every dimension, so more or
//     better data can never lower the level.
//   - A Gate adds hysteresis: it narrows at once, and widens one level at a time, only after
//     WidenAfter consecutive assessments above its current level. It starts at OBSERVE_ONLY:
//     authority is earned from evidence after a restart, never assumed.
//
// # Not in this experiment
//
// Provenance is a caller-supplied class, not derived here from certificates or signatures.
// Coverage is a plain share of targets, not weighted by criticality or by area (the outcome
// work weighs areas). Nothing is persisted, and no service enforces the result yet.
package obsgate

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Level is how much of a granted authority may be used for new actions. Levels are ordered.
type Level int

const (
	// ObserveOnly: no new actions.
	ObserveOnly Level = iota
	// Reduced: new actions within whatever narrower envelope the caller defines.
	Reduced
	// Full: the whole granted authority.
	Full
)

func (l Level) String() string {
	switch l {
	case ObserveOnly:
		return "OBSERVE_ONLY"
	case Reduced:
		return "REDUCED"
	case Full:
		return "FULL"
	}
	return fmt.Sprintf("Level(%d)", int(l))
}

func (l Level) valid() bool { return l >= ObserveOnly && l <= Full }

// Provenance is how far a reading's source can be trusted. Provenances are ordered.
type Provenance int

const (
	// Unverified: nothing ties the reading to the device (a relay, a derived value, an import).
	Unverified Provenance = iota
	// Authenticated: the device authenticated the session that carried it.
	Authenticated
	// Attested: the reading itself is signed or attested by the device.
	Attested
)

func (p Provenance) valid() bool { return p >= Unverified && p <= Attested }

// Observation is one reading of one target.
type Observation struct {
	Target     string
	At         time.Time
	Provenance Provenance
}

// Requirement is what a level needs from the evidence.
type Requirement struct {
	// MaxAge is how old a target's newest qualifying reading may be. Greater than zero.
	MaxAge time.Duration
	// MinCoverage is the share of the scope that must meet the requirement, in (0, 1].
	MinCoverage float64
	// MinProvenance is the weakest provenance that counts.
	MinProvenance Provenance
}

// Policy ties a granted level to observability.
type Policy struct {
	// Granted is the most the mandate allows. The gate never exceeds it.
	Granted Level
	Full    Requirement
	Reduced Requirement
	// MaxClockSkew is how far in the future a reading may be stamped and still count.
	MaxClockSkew time.Duration
	// WidenAfter is how many consecutive assessments above the gate's level it takes to widen
	// by one level. At least 1.
	WidenAfter int
}

// ErrInvalidPolicy is wrapped by every policy refusal.
var ErrInvalidPolicy = errors.New("invalid observability policy")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}

func (r Requirement) validate(name string) error {
	if r.MaxAge <= 0 {
		return invalid("%s: maxAge must be greater than zero", name)
	}
	// Written so a NaN fails too.
	if !(r.MinCoverage > 0 && r.MinCoverage <= 1) {
		return invalid("%s: minCoverage must be in (0, 1]", name)
	}
	if !r.MinProvenance.valid() {
		return invalid("%s: unknown provenance %d", name, r.MinProvenance)
	}
	return nil
}

// Validate refuses a policy that could not be applied, or under which more data could lower the
// level.
func (p Policy) Validate() error {
	if !p.Granted.valid() {
		return invalid("unknown granted level %d", p.Granted)
	}
	if err := p.Full.validate("full"); err != nil {
		return err
	}
	if err := p.Reduced.validate("reduced"); err != nil {
		return err
	}
	if p.Full.MaxAge > p.Reduced.MaxAge || p.Full.MinCoverage < p.Reduced.MinCoverage ||
		p.Full.MinProvenance < p.Reduced.MinProvenance {
		return invalid("the full requirement must be at least as strict as reduced in every dimension")
	}
	if p.MaxClockSkew < 0 {
		return invalid("maxClockSkew is negative")
	}
	if p.WidenAfter < 1 {
		return invalid("widenAfter must be at least 1")
	}
	return nil
}

func (p Policy) requirement(l Level) Requirement {
	if l == Full {
		return p.Full
	}
	return p.Reduced
}

// Assessment is what the evidence supports at one instant.
type Assessment struct {
	// Level is the scope's level, capped by the granted level.
	Level Level
	// Supported is the scope's level before the cap: what the evidence alone would allow.
	Supported Level
	// Scope is the number of targets assessed.
	Scope int
	// Coverage is, per level above OBSERVE_ONLY, the share of the scope meeting that level's
	// requirement. An empty scope has none.
	Coverage map[Level]float64
	// Unobserved lists, sorted, the targets that meet no requirement at all.
	Unobserved []string

	targets map[string]Level
}

// TargetLevel is the highest level whose requirement the target itself meets. A target outside
// the scope is OBSERVE_ONLY.
func (a Assessment) TargetLevel(target string) Level { return a.targets[target] }

// Assess judges the scope's observability at now.
func Assess(pol Policy, scope []string, obs []Observation, now time.Time) (Assessment, error) {
	if err := pol.Validate(); err != nil {
		return Assessment{}, err
	}
	targets := make(map[string]Level, len(scope))
	for _, t := range scope {
		if t == "" {
			return Assessment{}, errors.New("obsgate: empty target in scope")
		}
		if _, dup := targets[t]; dup {
			return Assessment{}, fmt.Errorf("obsgate: target %q appears twice in scope", t)
		}
		targets[t] = ObserveOnly
	}
	horizon := now.Add(pol.MaxClockSkew)
	meets := func(o Observation, r Requirement) bool {
		return !o.At.After(horizon) && now.Sub(o.At) <= r.MaxAge && o.Provenance >= r.MinProvenance
	}
	for _, o := range obs {
		cur, inScope := targets[o.Target]
		if !inScope || !o.Provenance.valid() {
			continue
		}
		for _, l := range []Level{Full, Reduced} {
			if l <= cur {
				break
			}
			if meets(o, pol.requirement(l)) {
				targets[o.Target] = l
				break
			}
		}
	}
	a := Assessment{Scope: len(scope), targets: targets, Coverage: map[Level]float64{}}
	if len(scope) == 0 {
		a.Coverage = nil
		return a, nil
	}
	counts := map[Level]int{}
	for t, l := range targets {
		if l == ObserveOnly {
			a.Unobserved = append(a.Unobserved, t)
		}
		for m := Reduced; m <= l; m++ {
			counts[m]++
		}
	}
	sort.Strings(a.Unobserved)
	for _, l := range []Level{Reduced, Full} {
		a.Coverage[l] = float64(counts[l]) / float64(len(scope))
	}
	for _, l := range []Level{Full, Reduced} {
		// Compared on counts, not on the division. The tolerance is far below one target: it
		// absorbs float rounding (0.68 x 75 is 51.00000000000001), never a real shortfall.
		// A positive requirement always needs at least one target: without the first
		// clause a requirement within the tolerance of zero would pass with no data.
		if counts[l] > 0 && float64(counts[l]) >= pol.requirement(l).MinCoverage*float64(len(scope))-1e-9 {
			a.Supported = l
			break
		}
	}
	a.Level = min(a.Supported, pol.Granted)
	return a, nil
}

// Gate carries the hysteresis between assessments. It is not safe for concurrent use.
type Gate struct {
	pol    Policy
	level  Level
	streak int
}

// NewGate builds a gate at OBSERVE_ONLY.
func NewGate(pol Policy) (*Gate, error) {
	if err := pol.Validate(); err != nil {
		return nil, err
	}
	return &Gate{pol: pol, level: ObserveOnly}, nil
}

// Level is the gate's current level.
func (g *Gate) Level() Level { return g.level }

// Step feeds one assessment and returns the gate's level after it: lower at once, higher by
// one level after WidenAfter consecutive assessments above the current level.
func (g *Gate) Step(a Assessment) Level {
	// Capped again here: an assessment made under another policy's grant must not carry the
	// gate past its own.
	l := min(a.Level, g.pol.Granted)
	switch {
	case l < g.level:
		g.level, g.streak = l, 0
	case l > g.level:
		g.streak++
		if g.streak >= g.pol.WidenAfter {
			g.level, g.streak = g.level+1, 0
		}
	default:
		g.streak = 0
	}
	return g.level
}

// Allows reports whether a new action needing level need may be taken on target, given the
// gate's current level and the assessment it was last stepped with. Above OBSERVE_ONLY both
// the scope (through the gate) and the target itself must support the level.
func (g *Gate) Allows(a Assessment, target string, need Level) bool {
	if need <= ObserveOnly {
		return true
	}
	return need <= g.level && a.TargetLevel(target) >= need
}
