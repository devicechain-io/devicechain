// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package obsgate

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 9, 18, 0, 0, 0, time.UTC)

func policy() Policy {
	return Policy{
		Granted:      Full,
		Full:         Requirement{MaxAge: 5 * time.Minute, MinCoverage: 0.9, MinProvenance: Authenticated},
		Reduced:      Requirement{MaxAge: 15 * time.Minute, MinCoverage: 0.6, MinProvenance: Unverified},
		MaxClockSkew: 30 * time.Second,
		WidenAfter:   3,
	}
}

func scope(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("lamp-%04d", i)
	}
	return out
}

// fresh gives the first k targets of the scope a reading of the given age and provenance.
func fresh(k int, age time.Duration, p Provenance) []Observation {
	var out []Observation
	for _, t := range scope(k) {
		out = append(out, Observation{Target: t, At: now.Add(-age), Provenance: p})
	}
	return out
}

func assess(t *testing.T, pol Policy, sc []string, obs []Observation) Assessment {
	t.Helper()
	a, err := Assess(pol, sc, obs, now)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPolicyValidation(t *testing.T) {
	if err := policy().Validate(); err != nil {
		t.Fatalf("reference policy: %v", err)
	}
	nan := math.NaN()
	bad := map[string]func(*Policy){
		"granted below":          func(p *Policy) { p.Granted = -1 },
		"granted above":          func(p *Policy) { p.Granted = Full + 1 },
		"zero full age":          func(p *Policy) { p.Full.MaxAge = 0 },
		"zero reduced age":       func(p *Policy) { p.Reduced.MaxAge = 0 },
		"zero coverage":          func(p *Policy) { p.Reduced.MinCoverage = 0 },
		"coverage above one":     func(p *Policy) { p.Full.MinCoverage = 1.01 },
		"nan coverage":           func(p *Policy) { p.Full.MinCoverage = nan },
		"unknown provenance":     func(p *Policy) { p.Full.MinProvenance = Attested + 1 },
		"full looser on age":     func(p *Policy) { p.Full.MaxAge = p.Reduced.MaxAge + 1 },
		"full looser on cover":   func(p *Policy) { p.Full.MinCoverage = p.Reduced.MinCoverage - 0.01 },
		"full looser provenance": func(p *Policy) { p.Reduced.MinProvenance = Attested },
		"negative skew":          func(p *Policy) { p.MaxClockSkew = -1 },
		"zero widen":             func(p *Policy) { p.WidenAfter = 0 },
	}
	for name, change := range bad {
		p := policy()
		change(&p)
		if err := p.Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: %v, want ErrInvalidPolicy", name, err)
		}
		if _, err := Assess(p, scope(3), nil, now); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: Assess accepted the policy", name)
		}
		if _, err := NewGate(p); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("%s: NewGate accepted the policy", name)
		}
	}
	// Equal requirements are allowed: "at least as strict" includes equal.
	p := policy()
	p.Full = p.Reduced
	if err := p.Validate(); err != nil {
		t.Errorf("equal requirements refused: %v", err)
	}
}

func TestLevelsFollowCoverage(t *testing.T) {
	sc := scope(10)
	cases := []struct {
		name string
		obs  []Observation
		want Level
		full float64
		red  float64
	}{
		{"all fresh and authenticated", fresh(10, time.Minute, Authenticated), Full, 1, 1},
		{"exactly 90 %", fresh(9, time.Minute, Authenticated), Full, 0.9, 0.9},
		{"80 %", fresh(8, time.Minute, Authenticated), Reduced, 0.8, 0.8},
		{"exactly 60 %", fresh(6, time.Minute, Authenticated), Reduced, 0.6, 0.6},
		{"50 %", fresh(5, time.Minute, Authenticated), ObserveOnly, 0.5, 0.5},
		{"nothing", nil, ObserveOnly, 0, 0},
		{"all fresh but unverified", fresh(10, time.Minute, Unverified), Reduced, 0, 1},
		{"all ten minutes old", fresh(10, 10*time.Minute, Attested), Reduced, 0, 1},
		{"exactly at full max age", fresh(10, 5*time.Minute, Authenticated), Full, 1, 1},
		{"all twenty minutes old", fresh(10, 20*time.Minute, Attested), ObserveOnly, 0, 0},
	}
	for _, c := range cases {
		a := assess(t, policy(), sc, c.obs)
		if a.Level != c.want || a.Supported != c.want || a.Coverage[Full] != c.full || a.Coverage[Reduced] != c.red {
			t.Errorf("%s: level %s cov full %.2f reduced %.2f; want %s %.2f %.2f",
				c.name, a.Level, a.Coverage[Full], a.Coverage[Reduced], c.want, c.full, c.red)
		}
		if a.Scope != 10 {
			t.Errorf("%s: scope %d", c.name, a.Scope)
		}
	}
}

func TestTheGrantCapsTheLevel(t *testing.T) {
	p := policy()
	p.Granted = Reduced
	a := assess(t, p, scope(4), fresh(4, time.Minute, Attested))
	if a.Supported != Full || a.Level != Reduced {
		t.Fatalf("supported %s level %s, want FULL capped to REDUCED", a.Supported, a.Level)
	}
	p.Granted = ObserveOnly
	if a := assess(t, p, scope(4), fresh(4, time.Minute, Attested)); a.Level != ObserveOnly {
		t.Fatalf("observe-only grant: %s", a.Level)
	}
}

func TestAReadingFromTheFutureCountsForNothing(t *testing.T) {
	sc := scope(1)
	at := func(d time.Duration) []Observation {
		return []Observation{{Target: sc[0], At: now.Add(d), Provenance: Attested}}
	}
	if a := assess(t, policy(), sc, at(30*time.Second)); a.Level != Full {
		t.Errorf("exactly at the skew: %s, want FULL", a.Level)
	}
	if a := assess(t, policy(), sc, at(31*time.Second)); a.Level != ObserveOnly || len(a.Unobserved) != 1 {
		t.Errorf("beyond the skew: %s %v, want OBSERVE_ONLY with the target unobserved", a.Level, a.Unobserved)
	}
	// A future-stamped reading does not displace a genuine one either.
	obs := append(at(time.Hour), Observation{Target: sc[0], At: now.Add(-time.Minute), Provenance: Attested})
	if a := assess(t, policy(), sc, obs); a.Level != Full {
		t.Errorf("genuine reading beside a future one: %s", a.Level)
	}
}

func TestTheBestReadingPerTargetCounts(t *testing.T) {
	sc := scope(1)
	obs := []Observation{
		{Target: sc[0], At: now.Add(-time.Minute), Provenance: Unverified},   // fresh, weak
		{Target: sc[0], At: now.Add(-4 * time.Minute), Provenance: Attested}, // older, strong
		{Target: sc[0], At: now.Add(-time.Hour), Provenance: Attested},       // stale
	}
	a := assess(t, policy(), sc, obs)
	if a.TargetLevel(sc[0]) != Full {
		t.Fatalf("target level %s, want FULL from the older attested reading", a.TargetLevel(sc[0]))
	}
	// Order of the readings does not matter.
	rev := []Observation{obs[2], obs[1], obs[0]}
	if b := assess(t, policy(), sc, rev); b.TargetLevel(sc[0]) != Full {
		t.Fatal("reading order changed the verdict")
	}
}

func TestOutOfScopeAndInvalidReadingsAreIgnored(t *testing.T) {
	sc := scope(2)
	obs := []Observation{
		{Target: "elsewhere", At: now, Provenance: Attested},
		{Target: sc[0], At: now, Provenance: Attested + 1},
		{Target: sc[1], At: now, Provenance: -1},
	}
	a := assess(t, policy(), sc, obs)
	if a.Level != ObserveOnly || len(a.Unobserved) != 2 || a.TargetLevel("elsewhere") != ObserveOnly {
		t.Fatalf("%+v", a)
	}
	if a.Unobserved[0] != sc[0] || a.Unobserved[1] != sc[1] {
		t.Fatalf("unobserved not sorted: %v", a.Unobserved)
	}
}

func TestScopeRefusalsAndTheEmptyScope(t *testing.T) {
	if _, err := Assess(policy(), []string{"a", "a"}, nil, now); err == nil {
		t.Error("duplicate target accepted")
	}
	if _, err := Assess(policy(), []string{"a", ""}, nil, now); err == nil {
		t.Error("empty target accepted")
	}
	a := assess(t, policy(), nil, fresh(3, time.Minute, Attested))
	if a.Level != ObserveOnly || a.Coverage != nil || a.Scope != 0 {
		t.Fatalf("empty scope: %+v, want OBSERVE_ONLY with no coverage", a)
	}
}

func TestMoreDataNeverLowersTheLevel(t *testing.T) {
	// Monotonicity over a small exhaustive space: adding any one reading to any observation
	// set never lowers the assessed level or any target's level.
	sc := scope(3)
	ages := []time.Duration{-time.Hour, 0, 4 * time.Minute, 10 * time.Minute, time.Hour}
	var pool []Observation
	for _, tg := range sc {
		for _, age := range ages {
			for p := Unverified; p <= Attested; p++ {
				pool = append(pool, Observation{Target: tg, At: now.Add(-age), Provenance: p})
			}
		}
	}
	for i := 0; i < len(pool); i += 7 {
		base := pool[:i]
		a := assess(t, policy(), sc, base)
		for _, extra := range pool {
			b := assess(t, policy(), sc, append(append([]Observation(nil), base...), extra))
			if b.Level < a.Level {
				t.Fatalf("adding %+v lowered %s to %s", extra, a.Level, b.Level)
			}
			for _, tg := range sc {
				if b.TargetLevel(tg) < a.TargetLevel(tg) {
					t.Fatalf("adding %+v lowered %s", extra, tg)
				}
			}
		}
	}
}

func TestGateNarrowsAtOnceAndWidensSlowly(t *testing.T) {
	g, err := NewGate(policy())
	if err != nil {
		t.Fatal(err)
	}
	if g.Level() != ObserveOnly {
		t.Fatalf("a new gate starts at %s, want OBSERVE_ONLY", g.Level())
	}
	full := Assessment{Level: Full}
	reduced := Assessment{Level: Reduced}
	dark := Assessment{Level: ObserveOnly}
	steps := []struct {
		a    Assessment
		want Level
	}{
		{full, ObserveOnly}, {full, ObserveOnly}, {full, Reduced}, // three to widen one level
		{full, Reduced}, {full, Reduced}, {full, Full}, // three more for the next
		{dark, ObserveOnly},                                           // narrows at once, two levels
		{full, ObserveOnly}, {full, ObserveOnly}, {dark, ObserveOnly}, // a dip resets the streak
		{full, ObserveOnly}, {full, ObserveOnly}, {full, Reduced},
		{reduced, Reduced}, // equal: no widening, streak reset
		{full, Reduced}, {full, Reduced}, {reduced, Reduced}, {full, Reduced}, {full, Reduced}, {full, Full},
		{reduced, Reduced}, // narrows one level
	}
	for i, s := range steps {
		if got := g.Step(s.a); got != s.want || g.Level() != s.want {
			t.Fatalf("step %d: %s, want %s", i, got, s.want)
		}
	}
}

func TestWidenAfterOneWidensOnEveryAssessment(t *testing.T) {
	p := policy()
	p.WidenAfter = 1
	g, _ := NewGate(p)
	if g.Step(Assessment{Level: Full}) != Reduced || g.Step(Assessment{Level: Full}) != Full {
		t.Fatal("WidenAfter 1 should widen one level per assessment")
	}
}

func TestAllowsNeedsTheScopeAndTheTarget(t *testing.T) {
	p := policy()
	p.WidenAfter = 1
	g, _ := NewGate(p)
	sc := scope(10)
	// Nine of ten observed: the scope is FULL, the tenth target is not observed at all.
	a := assess(t, p, sc, fresh(9, time.Minute, Authenticated))
	g.Step(a)
	g.Step(a)
	if g.Level() != Full {
		t.Fatalf("setup: gate at %s", g.Level())
	}
	if !g.Allows(a, sc[0], Full) || !g.Allows(a, sc[0], Reduced) {
		t.Error("an observed target in a FULL scope is refused")
	}
	if g.Allows(a, sc[9], Reduced) {
		t.Error("an unobserved target is allowed an action because the scope is well observed")
	}
	if !g.Allows(a, sc[9], ObserveOnly) {
		t.Error("observing is always allowed")
	}
	if g.Allows(a, "not-in-scope", Reduced) {
		t.Error("a target outside the scope is allowed")
	}
	if g.Allows(a, sc[0], Full+1) {
		t.Error("a level above FULL is allowed")
	}
	// The gate's own level bounds the target: a fresh gate allows nothing.
	g2, _ := NewGate(p)
	if g2.Allows(a, sc[0], Reduced) {
		t.Error("a gate at OBSERVE_ONLY allowed an action")
	}
}

func TestLevelStrings(t *testing.T) {
	for l, s := range map[Level]string{ObserveOnly: "OBSERVE_ONLY", Reduced: "REDUCED", Full: "FULL", 7: "Level(7)"} {
		if l.String() != s {
			t.Errorf("%d: %s", int(l), l.String())
		}
	}
}

func TestCoverageJustBelowTheThresholdDoesNotPass(t *testing.T) {
	// 899 of 1000 is 0.1 percentage points short of FULL's 90 %: the tolerance on the
	// comparison absorbs float rounding, never a real shortfall.
	sc := scope(1000)
	var obs []Observation
	for _, tg := range sc[:899] {
		obs = append(obs, Observation{Target: tg, At: now, Provenance: Attested})
	}
	if a := assess(t, policy(), sc, obs); a.Level != Reduced {
		t.Fatalf("899/1000: %s, want REDUCED", a.Level)
	}
	obs = append(obs, Observation{Target: sc[899], At: now, Provenance: Attested})
	if a := assess(t, policy(), sc, obs); a.Level != Full {
		t.Fatalf("900/1000: %s, want FULL", a.Level)
	}
}

func TestTheToleranceIsForRoundingOnly(t *testing.T) {
	// 9 of 10 is 0.9, short of a 0.9001 requirement by a tenth of a target.
	p := policy()
	p.Full.MinCoverage = 0.9001
	if a := assess(t, p, scope(10), fresh(9, time.Minute, Attested)); a.Level != Reduced {
		t.Fatalf("9/10 against 0.9001: %s, want REDUCED", a.Level)
	}
	// And 0.68 of 75, which float64 computes as 51.00000000000001, passes at exactly 51.
	p = policy()
	p.Reduced.MinCoverage = 0.68
	if a := assess(t, p, scope(75), fresh(51, 10*time.Minute, Attested)); a.Level != Reduced {
		t.Fatalf("51/75 against 0.68: %s, want REDUCED", a.Level)
	}
}

func TestNoDataNeverPassesATinyRequirement(t *testing.T) {
	p := policy()
	p.Full.MinCoverage, p.Reduced.MinCoverage = 1e-10, 1e-10
	if a := assess(t, p, scope(1), nil); a.Level != ObserveOnly {
		t.Fatalf("no readings against a 1e-10 requirement: %s, want OBSERVE_ONLY", a.Level)
	}
}

func TestTheGateNeverExceedsItsOwnGrant(t *testing.T) {
	p := policy()
	p.Granted = Reduced
	p.WidenAfter = 1
	g, _ := NewGate(p)
	for i := 0; i < 5; i++ {
		g.Step(Assessment{Level: Full}) // as if assessed under a FULL grant
	}
	if g.Level() != Reduced {
		t.Fatalf("gate granted REDUCED reached %s", g.Level())
	}
}
