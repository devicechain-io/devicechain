// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package obssim drives the observability gate through simulated fleets, so the rules can be
// judged on scenarios (a district going dark, a backhaul outage, a flapping link, a clock that
// runs ahead, a relay that strips provenance) before any service enforces them.
//
// Every scenario is deterministic: no randomness, no wall clock. Each tick, every node may send
// one reading; the controller then wants to act on every node, and the run counts the actions
// the gate let through and, among them, the BLIND ones: actions on a node whose last GENUINE
// reading (what the node really sent, by the simulator's own truth, not by its timestamp) is
// older than the reduced requirement's maximum age. A naive controller, which acts whenever the
// mandate grants it, is run beside the gate on the same fleet as the control.
package obssim

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/obsgate"
)

// Reading is what one node sends on one tick, if anything.
type Reading struct {
	Sends bool
	// Provenance of the reading.
	Provenance obsgate.Provenance
	// StampOffset is added to the true time to give the reading's timestamp (a clock that
	// runs ahead has a positive offset).
	StampOffset time.Duration
	// Genuine is false for a reading that carries no new observation (a relay replaying a
	// cached value). It still reaches the gate; it does not refresh the simulator's truth.
	Genuine bool
}

// Scenario is one simulated fleet.
type Scenario struct {
	Name      string
	Nodes     int
	Districts int
	Ticks     int
	Tick      time.Duration
	// Send decides what node n (in district n % Districts) sends on tick t.
	Send func(t, n, district int) Reading
}

// Result is one controller's record over a run.
type Result struct {
	// Levels is the controller's level on each tick.
	Levels []obsgate.Level
	// Changes counts level changes between consecutive ticks.
	Changes int
	// Actions counts actions taken; Blind counts those taken on a node with no genuine
	// reading inside the reduced requirement's maximum age.
	Actions int
	Blind   int
}

// Run plays the scenario twice over the same fleet, once through the gate and once through the
// naive controller, and returns both. The controller needs REDUCED to act.
func Run(pol obsgate.Policy, sc Scenario) (gated, naive Result, err error) {
	if sc.Nodes < 1 || sc.Districts < 1 || sc.Ticks < 1 || sc.Tick <= 0 || sc.Send == nil {
		return Result{}, Result{}, fmt.Errorf("obssim: scenario %q is incomplete", sc.Name)
	}
	gate, err := obsgate.NewGate(pol)
	if err != nil {
		return Result{}, Result{}, err
	}
	start := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	scope := make([]string, sc.Nodes)
	for n := range scope {
		scope[n] = fmt.Sprintf("node-%04d", n)
	}
	var obs []obsgate.Observation
	lastGenuine := make([]time.Time, sc.Nodes)

	for t := 0; t < sc.Ticks; t++ {
		now := start.Add(time.Duration(t) * sc.Tick)
		for n := 0; n < sc.Nodes; n++ {
			r := sc.Send(t, n, n%sc.Districts)
			if !r.Sends {
				continue
			}
			obs = append(obs, obsgate.Observation{Target: scope[n], At: now.Add(r.StampOffset), Provenance: r.Provenance})
			if r.Genuine {
				lastGenuine[n] = now
			}
		}
		// Keep only each node's newest few readings: the assessment needs no more, and the
		// run stays linear in its length.
		obs = trim(obs, sc.Nodes)

		a, err := obsgate.Assess(pol, scope, obs, now)
		if err != nil {
			return Result{}, Result{}, err
		}
		gate.Step(a)
		blind := func(n int) bool {
			return lastGenuine[n].IsZero() || now.Sub(lastGenuine[n]) > pol.Reduced.MaxAge
		}
		record := func(res *Result, level obsgate.Level, allows func(n int) bool) {
			if len(res.Levels) > 0 && res.Levels[len(res.Levels)-1] != level {
				res.Changes++
			}
			res.Levels = append(res.Levels, level)
			for n := 0; n < sc.Nodes; n++ {
				if allows(n) {
					res.Actions++
					if blind(n) {
						res.Blind++
					}
				}
			}
		}
		record(&gated, gate.Level(), func(n int) bool { return gate.Allows(a, scope[n], obsgate.Reduced) })
		record(&naive, pol.Granted, func(int) bool { return pol.Granted >= obsgate.Reduced })
	}
	return gated, naive, nil
}

// trim keeps, per target AND per provenance, the readings from the last few appends.
// Readings are appended in order of their true time, so the tail holds the newest of each
// class. Per provenance, because the gate deliberately counts an older reading of stronger
// provenance: a node's last authenticated reading must survive a run of unverified ones.
func trim(obs []obsgate.Observation, nodes int) []obsgate.Observation {
	const keep = 4
	classes := int(obsgate.Attested) + 1
	if len(obs) <= keep*nodes*classes*2 {
		return obs
	}
	type class struct {
		target string
		p      obsgate.Provenance
	}
	seen := map[class]int{}
	out := make([]obsgate.Observation, 0, keep*nodes*classes)
	for i := len(obs) - 1; i >= 0; i-- {
		c := class{obs[i].Target, obs[i].Provenance}
		if seen[c] < keep {
			seen[c]++
			out = append(out, obs[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
