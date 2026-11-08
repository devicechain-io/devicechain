// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package obssim

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/obsgate"
)

func policy() obsgate.Policy {
	return obsgate.Policy{
		Granted:      obsgate.Full,
		Full:         obsgate.Requirement{MaxAge: 2 * time.Minute, MinCoverage: 0.95, MinProvenance: obsgate.Authenticated},
		Reduced:      obsgate.Requirement{MaxAge: 5 * time.Minute, MinCoverage: 0.7, MinProvenance: obsgate.Unverified},
		MaxClockSkew: 30 * time.Second,
		WidenAfter:   3,
	}
}

func healthy(int, int, int) Reading {
	return Reading{Sends: true, Provenance: obsgate.Authenticated, Genuine: true}
}

// fleet is 80 nodes over 4 districts, one reading a minute, for two hours.
func fleet(name string, send func(t, n, d int) Reading) Scenario {
	return Scenario{Name: name, Nodes: 80, Districts: 4, Ticks: 120, Tick: time.Minute, Send: send}
}

func run(t *testing.T, sc Scenario) (Result, Result) {
	t.Helper()
	g, n, err := Run(policy(), sc)
	if err != nil {
		t.Fatal(err)
	}
	return g, n
}

func TestHealthyFleetEarnsFullAuthority(t *testing.T) {
	g, n := run(t, fleet("healthy", healthy))
	if g.Levels[len(g.Levels)-1] != obsgate.Full || g.Blind != 0 || n.Blind != 0 {
		t.Fatalf("gated %+v naive blind %d", g.Levels[len(g.Levels)-1], n.Blind)
	}
	// Earned, not assumed: OBSERVE_ONLY on the first tick, REDUCED and then FULL after the
	// widening streaks, and no change after that.
	if g.Levels[0] != obsgate.ObserveOnly || g.Levels[2] != obsgate.Reduced || g.Levels[5] != obsgate.Full || g.Changes != 2 {
		t.Fatalf("levels %v, changes %d", g.Levels[:8], g.Changes)
	}
}

// 🔴 THE NEGATIVE CONTROL. One district of four goes dark at tick 30. Coverage stays at 75 %,
// above the reduced requirement, so a scope-only rule would keep acting on the dark district.
// The naive controller does exactly that and takes blind actions; the gate takes none, while
// still acting on the three districts it can see.
func TestADarkDistrictIsNotActedOnBlind(t *testing.T) {
	g, n := run(t, fleet("dark district", func(t, node, d int) Reading {
		if d == 2 && t >= 30 {
			return Reading{}
		}
		return healthy(t, node, d)
	}))
	if n.Blind == 0 {
		t.Fatal("control: the naive controller took no blind action, so the scenario proves nothing")
	}
	if g.Blind != 0 {
		t.Fatalf("the gate took %d blind actions", g.Blind)
	}
	if g.Levels[len(g.Levels)-1] != obsgate.Reduced {
		t.Fatalf("final level %s, want REDUCED (75 %% covered)", g.Levels[len(g.Levels)-1])
	}
	if g.Actions == 0 {
		t.Fatal("the gate stopped acting altogether; it should keep acting on the observed districts")
	}
}

func TestABackhaulOutageNarrowsAndRecoversSlowly(t *testing.T) {
	g, n := run(t, fleet("backhaul outage", func(t, node, d int) Reading {
		if t >= 40 && t < 60 {
			return Reading{}
		}
		return healthy(t, node, d)
	}))
	if n.Blind == 0 || g.Blind != 0 {
		t.Fatalf("blind: naive %d (want > 0), gated %d (want 0)", n.Blind, g.Blind)
	}
	// Before the outage FULL; FULL lapses once readings pass the full max age, OBSERVE_ONLY
	// once they pass the reduced one; FULL again only after two widening streaks.
	want := map[int]obsgate.Level{39: obsgate.Full, 42: obsgate.Reduced, 45: obsgate.ObserveOnly, 59: obsgate.ObserveOnly, 61: obsgate.ObserveOnly, 62: obsgate.Reduced, 65: obsgate.Full}
	for tick, l := range want {
		if g.Levels[tick] != l {
			t.Errorf("tick %d: %s, want %s", tick, g.Levels[tick], l)
		}
	}
}

func TestAFlappingLinkDoesNotEarnFullAuthorityBack(t *testing.T) {
	// The whole fleet's link is up for 2 minutes in every 8. Each cycle the raw evidence
	// supports FULL for 4 ticks, REDUCED for 3 and nothing for 1, so a gate that widened on
	// every assessment would climb to FULL and fall back to OBSERVE_ONLY every cycle. The
	// widening streak keeps it at or below REDUCED after the first cycle, and it changes level
	// about half as often.
	sc := fleet("flapping", func(t, node, d int) Reading {
		if t%8 >= 2 {
			return Reading{}
		}
		return healthy(t, node, d)
	})
	g, n := run(t, sc)
	eager := policy()
	eager.WidenAfter = 1
	e, _, err := Run(eager, sc)
	if err != nil {
		t.Fatal(err)
	}
	if e.Changes == 0 || g.Changes*2 > e.Changes+2 { // +2: the first cycle climbs from OBSERVE_ONLY
		t.Fatalf("gate changed level %d times, eager gate %d; want about half", g.Changes, e.Changes)
	}
	for tick := 8; tick < len(g.Levels); tick++ {
		if g.Levels[tick] == obsgate.Full {
			t.Fatalf("tick %d: the flapping link earned FULL back", tick)
		}
	}
	reachedFull := false
	for _, l := range e.Levels[8:] {
		reachedFull = reachedFull || l == obsgate.Full
	}
	if !reachedFull {
		t.Fatal("control: the eager gate never reached FULL, so the scenario proves nothing")
	}
	if g.Blind != 0 || n.Blind == 0 {
		t.Fatalf("blind: gated %d (want 0), naive %d (want > 0)", g.Blind, n.Blind)
	}
}

func TestAClockRunningAheadIsNotFreshness(t *testing.T) {
	// District 1 stops sending at tick 30, but a relay replays its last value every minute
	// stamped an hour ahead. Without the skew rule the stamps would read as fresh forever.
	sc := fleet("clock ahead", func(t, node, d int) Reading {
		if d == 1 && t >= 30 {
			return Reading{Sends: true, Provenance: obsgate.Authenticated, StampOffset: time.Hour}
		}
		return healthy(t, node, d)
	})
	g, n := run(t, sc)
	if n.Blind == 0 || g.Blind != 0 {
		t.Fatalf("blind: naive %d (want > 0), gated %d (want 0)", n.Blind, g.Blind)
	}
	// Control on the rule itself: with a skew allowance wide enough to admit the stamps, the
	// gate is fooled, which is what the rule is there to prevent.
	loose := policy()
	loose.MaxClockSkew = 2 * time.Hour
	fooled, _, err := Run(loose, sc)
	if err != nil {
		t.Fatal(err)
	}
	if fooled.Blind == 0 {
		t.Fatal("control: a skew allowance of two hours should let the replayed stamps through")
	}
}

func TestStrippedProvenanceDropsFullButKeepsReduced(t *testing.T) {
	// From tick 50 every reading arrives through a relay that strips authentication.
	g, _ := run(t, fleet("stripped provenance", func(t, node, d int) Reading {
		r := healthy(t, node, d)
		if t >= 50 {
			r.Provenance = obsgate.Unverified
		}
		return r
	}))
	if g.Levels[49] != obsgate.Full || g.Levels[len(g.Levels)-1] != obsgate.Reduced {
		t.Fatalf("tick 49 %s, final %s; want FULL then REDUCED", g.Levels[49], g.Levels[len(g.Levels)-1])
	}
	if g.Blind != 0 {
		t.Fatalf("gated blind %d", g.Blind)
	}
}

func TestRunRefusesAnIncompleteScenario(t *testing.T) {
	for _, sc := range []Scenario{
		{Name: "no nodes", Districts: 1, Ticks: 1, Tick: time.Second, Send: healthy},
		{Name: "no districts", Nodes: 1, Ticks: 1, Tick: time.Second, Send: healthy},
		{Name: "no ticks", Nodes: 1, Districts: 1, Tick: time.Second, Send: healthy},
		{Name: "no tick", Nodes: 1, Districts: 1, Ticks: 1, Send: healthy},
		{Name: "no send", Nodes: 1, Districts: 1, Ticks: 1, Tick: time.Second},
	} {
		if _, _, err := Run(policy(), sc); err == nil {
			t.Errorf("%s: accepted", sc.Name)
		}
	}
	bad := policy()
	bad.WidenAfter = 0
	if _, _, err := Run(bad, fleet("x", healthy)); err == nil {
		t.Error("invalid policy accepted")
	}
}

func TestTrimKeepsEachTargetsNewest(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var obs []obsgate.Observation
	for i := 0; i < 40; i++ {
		obs = append(obs, obsgate.Observation{Target: "a", At: base.Add(time.Duration(i) * time.Minute)})
	}
	obs = append(obs, obsgate.Observation{Target: "b", At: base})
	out := trim(obs, 1)
	var a, b int
	for _, o := range out {
		if o.Target == "a" {
			a++
			if o.At.Before(base.Add(36 * time.Minute)) {
				t.Fatalf("kept an old reading of a: %s", o.At)
			}
		} else {
			b++
		}
	}
	if a != 4 || b != 1 {
		t.Fatalf("kept %d of a and %d of b, want 4 and 1", a, b)
	}
}

func TestTrimKeepsTheLastStrongReadingThroughWeakOnes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	obs := []obsgate.Observation{{Target: "a", At: base, Provenance: obsgate.Authenticated}}
	for i := 1; i <= 30; i++ {
		obs = append(obs, obsgate.Observation{Target: "a", At: base.Add(time.Duration(i) * time.Second), Provenance: obsgate.Unverified})
	}
	kept := false
	for _, o := range trim(obs, 1) {
		kept = kept || o.Provenance == obsgate.Authenticated
	}
	if !kept {
		t.Fatal("the only authenticated reading was trimmed away by newer unverified ones")
	}
}
