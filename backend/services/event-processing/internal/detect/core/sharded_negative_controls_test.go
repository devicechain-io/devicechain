// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"testing"
	"time"
)

// This file is the differential harness's NEGATIVE CONTROLS: a small K-way engine, built from K
// plain engines, that is correct in one mode and deliberately wrong in each of the others, and a
// test that requires the harness to FAIL for every wrong mode at every shard count.
//
// A differential that has only ever been shown green proves nothing: it may be comparing too little
// to notice a divergence, or comparing the wrong thing. These controls are the proof that it is not.
// They are also the specification of what a sharded engine must get right, one hazard per mode:
//
//   - per-shard-watermarks: a shard ticks only on its own events, so the frontier is local
//   - correlation-routed-by-device: an anchor's members are split across shards
//   - descope-after-tick: a departed series' timer fires before its descope is applied
//   - clearraised-routed-by-rule: the publish path's latch clear misses the shard that owns the key
//   - advance-skips-idle-shards: an idle advance reaches only the shards with work pending
//   - purge-one-shard: a tenant purge reaches one shard
//   - unsorted-raised-merge: the merged snapshot is not in canonical order
//   - restore-drops-expected: a restore loses dead-man arming (a restart leg only)
//   - flips-detection-edges: a raise is reported as a resolve and the reverse
//
// The "correct" mode is the control's own control: if it ever fails, the harness or this file is
// broken rather than the engine. None of this is the sharded engine; that is Sharded, in sharded.go.

// protoMode names the K-way engine's behaviour.
type protoMode int

const (
	protoCorrect protoMode = iota
	protoPerShardWM
	protoCorrByDevice
	protoDescopeAfterTick
	protoClearRaisedByRule
	protoAdvanceSkipsIdleShards
	protoPurgeOneShard
	protoUnsortedRaisedMerge
	protoRestoreDropsExpected
	protoFlipsEdges
)

func (m protoMode) String() string {
	return [...]string{"correct", "per-shard-watermarks", "correlation-routed-by-device", "descope-after-tick", "clearraised-routed-by-rule", "advance-skips-idle-shards", "purge-one-shard", "unsorted-raised-merge", "restore-drops-expected", "flips-detection-edges"}[m]
}

type protoSharded struct {
	mode     protoMode
	k        int
	lateness time.Duration
	shards   []*Engine
	pending  []func() // deferred descopes (descope-after-tick)
}

func protoHash(s ...string) int {
	h := fnv.New32a()
	for _, p := range s {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return int(h.Sum32() & 0x7fffffff)
}

func (p *protoSharded) owner(k SeriesKey) int { return protoHash(k.Rule, k.Series) % p.k }

func newProto(mode protoMode, k int, rules []Rule, lateness time.Duration) *protoSharded {
	p := &protoSharded{mode: mode, k: k, lateness: lateness}
	for i := 0; i < k; i++ {
		p.shards = append(p.shards, NewEngine(rules, lateness))
	}
	return p
}

func protoBuilder(mode protoMode, k int) engineBuilder {
	return engineBuilder{
		name:  fmt.Sprintf("proto-%s(K=%d)", mode, k),
		fresh: func(rules []Rule, l time.Duration) detectEngine { return newProto(mode, k, rules, l) },
		restore: func(rules []Rule, l time.Duration, snap []byte) (detectEngine, error) {
			p := newProto(mode, k, rules, l)
			var s snapshot
			if err := json.Unmarshal(snap, &s); err != nil {
				return nil, err
			}
			parts := make([]snapshot, k)
			for i := range parts {
				parts[i] = snapshot{Watermark: s.Watermark, LastSeq: s.LastSeq}
			}
			sh := func(r, se string) int { return protoHash(r, se) % k }
			for _, x := range s.Active {
				i := sh(x.Rule, x.Series)
				parts[i].Active = append(parts[i].Active, x)
			}
			for _, x := range s.Breaks {
				i := sh(x.Rule, x.Series)
				parts[i].Breaks = append(parts[i].Breaks, x)
			}
			for _, x := range s.Timers {
				i := sh(x.Rule, x.Series)
				parts[i].Timers = append(parts[i].Timers, x)
			}
			for _, x := range s.Gens {
				i := sh(x.Rule, x.Series)
				parts[i].Gens = append(parts[i].Gens, x)
			}
			for _, x := range s.Sliding {
				i := sh(x.Rule, x.Series)
				parts[i].Sliding = append(parts[i].Sliding, x)
			}
			for _, x := range s.Panes {
				i := sh(x.Rule, x.Series)
				parts[i].Panes = append(parts[i].Panes, x)
			}
			for _, x := range s.Deltas {
				i := sh(x.Rule, x.Series)
				parts[i].Deltas = append(parts[i].Deltas, x)
			}
			for _, x := range s.Counts {
				i := sh(x.Rule, x.Series)
				parts[i].Counts = append(parts[i].Counts, x)
			}
			for _, x := range s.Sessions {
				i := sh(x.Rule, x.Series)
				parts[i].Sessions = append(parts[i].Sessions, x)
			}
			for _, x := range s.Slides {
				i := sh(x.Rule, x.Series)
				parts[i].Slides = append(parts[i].Slides, x)
			}
			for _, x := range s.Corr {
				i := sh(x.Rule, x.Series)
				parts[i].Corr = append(parts[i].Corr, x)
			}
			if mode == protoRestoreDropsExpected {
				s.Expected = nil
			}
			for _, x := range s.Expected {
				i := sh(x.Rule, x.Series)
				parts[i].Expected = append(parts[i].Expected, x)
			}
			for _, x := range s.Raised {
				i := sh(x.Rule, x.Series)
				parts[i].Raised = append(parts[i].Raised, x)
			}
			for _, x := range s.Presence {
				i := sh(x.Rule, x.Series)
				parts[i].Presence = append(parts[i].Presence, x)
			}
			for i := range parts {
				b, _ := json.Marshal(parts[i])
				e, err := Restore(rules, l, b)
				if err != nil {
					return nil, err
				}
				p.shards[i] = e
			}
			return p, nil
		},
	}
}

func (p *protoSharded) UpsertRule(r Rule) {
	for _, s := range p.shards {
		s.UpsertRule(r)
	}
}
func (p *protoSharded) RemoveRule(id string) {
	for _, s := range p.shards {
		s.RemoveRule(id)
	}
}
func (p *protoSharded) RemoveMatching(m func(string) bool) int {
	n := 0
	for i, s := range p.shards {
		if p.mode == protoPurgeOneShard && i > 0 {
			continue
		}
		n += s.RemoveMatching(m)
	}
	return n
}

func (p *protoSharded) Descope(rule, series string, at time.Time) bool {
	o := p.shards[p.owner(SeriesKey{Rule: rule, Series: series})]
	if p.mode == protoDescopeAfterTick {
		// Report the true answer (probe on a restored copy) but apply the descope only AFTER the
		// next message's tick.
		snap, _ := o.Snapshot()
		probe, _ := Restore(rulesOf(o), p.lateness, snap)
		ans := probe.Descope(rule, series, at)
		p.pending = append(p.pending, func() { o.Descope(rule, series, at) })
		return ans
	}
	return o.Descope(rule, series, at)
}

func rulesOf(e *Engine) []Rule {
	out := make([]Rule, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, r)
	}
	return out
}

func (p *protoSharded) route(ev Event) int {
	if p.mode == protoCorrByDevice && ev.Member != "" {
		return protoHash(ev.Key.Rule, ev.Member) % p.k
	}
	return p.owner(ev.Key)
}

func (p *protoSharded) ProcessResolved(seq uint64, t time.Time, evs []Event) {
	per := make([][]Event, p.k)
	for _, ev := range evs {
		i := p.route(ev)
		per[i] = append(per[i], ev)
	}
	for i, s := range p.shards {
		if p.mode == protoPerShardWM && len(per[i]) == 0 {
			// local watermark: a shard that holds none of this message's keys still learns the
			// sequence (so lastSeq agrees) but NOT the time, so its frontier stays local.
			s.ProcessResolved(seq, s.Watermark().Add(p.lateness), nil)
			continue
		}
		s.ProcessResolved(seq, t, per[i])
	}
	for _, f := range p.pending {
		f()
	}
	p.pending = nil
}

func (p *protoSharded) Skip(seq uint64) bool {
	r := false
	for _, s := range p.shards {
		r = s.Skip(seq) || r
	}
	return r
}
func (p *protoSharded) Advance(w time.Time) bool {
	r := false
	for _, s := range p.shards {
		if p.mode == protoAdvanceSkipsIdleShards && !s.HasPendingWork() {
			continue
		}
		r = s.Advance(w) || r
	}
	return r
}
func (p *protoSharded) SetExpected(k SeriesKey, since time.Time) {
	p.shards[p.owner(k)].SetExpected(k, since)
}
func (p *protoSharded) RemoveExpected(k SeriesKey) { p.shards[p.owner(k)].RemoveExpected(k) }
func (p *protoSharded) ClearRaised(k SeriesKey) {
	if p.mode == protoClearRaisedByRule {
		p.shards[protoHash(k.Rule)%p.k].ClearRaised(k)
		return
	}
	p.shards[p.owner(k)].ClearRaised(k)
}
func (p *protoSharded) Drain() []Detection {
	var out []Detection
	for _, s := range p.shards {
		out = append(out, s.Drain()...)
	}
	if p.mode == protoFlipsEdges {
		for i := range out {
			out[i].Edge = 1 - out[i].Edge
		}
	}
	return out
}
func (p *protoSharded) Watermark() time.Time {
	w := p.shards[0].Watermark()
	for _, s := range p.shards {
		if s.Watermark().After(w) {
			w = s.Watermark()
		}
	}
	return w
}
func (p *protoSharded) LastSeq() uint64 {
	var m uint64
	for _, s := range p.shards {
		if s.LastSeq() > m {
			m = s.LastSeq()
		}
	}
	return m
}
func (p *protoSharded) HasPendingWork() bool {
	for _, s := range p.shards {
		if s.HasPendingWork() {
			return true
		}
	}
	return false
}
func (p *protoSharded) PendingTimerCount() int {
	n := 0
	for _, s := range p.shards {
		n += s.PendingTimerCount()
	}
	return n
}
func (p *protoSharded) LiveKeyCounts() map[string]int {
	out := map[string]int{}
	for _, s := range p.shards {
		for r, n := range s.LiveKeyCounts() {
			out[r] += n
		}
	}
	return out
}

func (p *protoSharded) Snapshot() ([]byte, error) {
	m := snapshot{Active: []snapRun{}, Breaks: []snapBreak{}, Timers: []snapTimer{}, Gens: []snapGen{}, Sliding: []snapSliding{}, Panes: []snapPane{}, Deltas: []snapDelta{}, Counts: []snapCount{}, Sessions: []snapSession{}, Slides: []snapSlide{}, Corr: []snapCorr{}, Expected: []snapExpected{}, Raised: []snapRaised{}, Presence: []snapPresence{}}
	for i, s := range p.shards {
		b, err := s.Snapshot()
		if err != nil {
			return nil, err
		}
		var x snapshot
		if err := json.Unmarshal(b, &x); err != nil {
			return nil, err
		}
		if i == 0 || x.Watermark.After(m.Watermark) {
			m.Watermark = x.Watermark
		}
		if x.LastSeq > m.LastSeq {
			m.LastSeq = x.LastSeq
		}
		m.Active = append(m.Active, x.Active...)
		m.Breaks = append(m.Breaks, x.Breaks...)
		m.Timers = append(m.Timers, x.Timers...)
		m.Gens = append(m.Gens, x.Gens...)
		m.Sliding = append(m.Sliding, x.Sliding...)
		m.Panes = append(m.Panes, x.Panes...)
		m.Deltas = append(m.Deltas, x.Deltas...)
		m.Counts = append(m.Counts, x.Counts...)
		m.Sessions = append(m.Sessions, x.Sessions...)
		m.Slides = append(m.Slides, x.Slides...)
		m.Corr = append(m.Corr, x.Corr...)
		m.Expected = append(m.Expected, x.Expected...)
		m.Raised = append(m.Raised, x.Raised...)
		m.Presence = append(m.Presence, x.Presence...)
	}
	sortByRuleSeries(m.Active, func(i int) (string, string) { return m.Active[i].Rule, m.Active[i].Series })
	sortByRuleSeries(m.Breaks, func(i int) (string, string) { return m.Breaks[i].Rule, m.Breaks[i].Series })
	sortByRuleSeries(m.Gens, func(i int) (string, string) { return m.Gens[i].Rule, m.Gens[i].Series })
	sortByRuleSeries(m.Sliding, func(i int) (string, string) { return m.Sliding[i].Rule, m.Sliding[i].Series })
	sortByRuleSeries(m.Deltas, func(i int) (string, string) { return m.Deltas[i].Rule, m.Deltas[i].Series })
	sortByRuleSeries(m.Counts, func(i int) (string, string) { return m.Counts[i].Rule, m.Counts[i].Series })
	sortByRuleSeries(m.Sessions, func(i int) (string, string) { return m.Sessions[i].Rule, m.Sessions[i].Series })
	sortByRuleSeries(m.Slides, func(i int) (string, string) { return m.Slides[i].Rule, m.Slides[i].Series })
	sortByRuleSeries(m.Corr, func(i int) (string, string) { return m.Corr[i].Rule, m.Corr[i].Series })
	sortByRuleSeries(m.Expected, func(i int) (string, string) { return m.Expected[i].Rule, m.Expected[i].Series })
	if p.mode != protoUnsortedRaisedMerge {
		sortByRuleSeries(m.Raised, func(i int) (string, string) { return m.Raised[i].Rule, m.Raised[i].Series })
	}
	sortByRuleSeries(m.Presence, func(i int) (string, string) { return m.Presence[i].Rule, m.Presence[i].Series })
	sort.Slice(m.Timers, func(i, j int) bool {
		a, b := m.Timers[i], m.Timers[j]
		if !a.Deadline.Equal(b.Deadline) {
			return a.Deadline.Before(b.Deadline)
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Series < b.Series
	})
	sort.Slice(m.Panes, func(i, j int) bool {
		a, b := m.Panes[i], m.Panes[j]
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		if a.Series != b.Series {
			return a.Series < b.Series
		}
		return a.Start < b.Start
	})
	return json.Marshal(m)
}

// restartAs is the shard count each K restarts at in the restart leg.
var controlRestartAs = map[int]int{2: 5, 3: 16, 7: 2, 16: 3}

// controlLeg runs one leg of the differential against a mode at a shard count and seed.
func controlLeg(mode protoMode, k int, seed int64, restart bool) error {
	var crash *diffCrash
	if restart {
		crash = &diffCrash{at: diffOpCount/4 + int(seed*37)%(diffOpCount/2), restoreAs: protoBuilder(mode, controlRestartAs[k])}
	}
	_, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), protoBuilder(mode, k), crash)
	return err
}

// TestHarnessAcceptsACorrectKWayEngine is the controls' own control: a real K-way engine built
// from K plain engines, with the canonical snapshot merged and split by key, passes every leg of
// the harness at every K. If this fails, a failure below means nothing.
func TestHarnessAcceptsACorrectKWayEngine(t *testing.T) {
	for _, k := range diffShardKs {
		for _, seed := range diffSeeds {
			for _, restart := range []bool{false, true} {
				if err := controlLeg(protoCorrect, k, seed, restart); err != nil {
					t.Fatalf("K=%d seed=%d restart=%v: the correct engine was rejected: %v", k, seed, restart, err)
				}
			}
		}
	}
}

// TestHarnessRejectsEveryBrokenKWayEngine requires the harness to fail for each wrong mode at
// EVERY shard count, on the leg the mode targets (restore-drops-expected is only visible across a
// restart; the rest are visible in a straight run). A mode need not fail for every seed, since one
// seed may never exercise the hazard, but it must fail for some seed at each K.
func TestHarnessRejectsEveryBrokenKWayEngine(t *testing.T) {
	for _, mode := range []protoMode{
		protoPerShardWM, protoCorrByDevice, protoDescopeAfterTick, protoClearRaisedByRule,
		protoAdvanceSkipsIdleShards, protoPurgeOneShard, protoUnsortedRaisedMerge,
		protoRestoreDropsExpected, protoFlipsEdges,
	} {
		restartLeg := mode == protoRestoreDropsExpected
		for _, k := range diffShardKs {
			mode, k := mode, k
			t.Run(fmt.Sprintf("%s/K=%d", mode, k), func(t *testing.T) {
				var detected error
				failed := 0
				for _, seed := range diffSeeds {
					if err := controlLeg(mode, k, seed, restartLeg); err != nil {
						failed++
						if detected == nil {
							detected = err
						}
					}
				}
				if detected == nil {
					t.Fatalf("the harness accepted a K=%d engine that %s, on all %d seeds: it cannot see this defect", k, mode, len(diffSeeds))
				}
				t.Logf("rejected on %d of %d seeds; first: %.200s", failed, len(diffSeeds), detected)
			})
		}
	}
}
