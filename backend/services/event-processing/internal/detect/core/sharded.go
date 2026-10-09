// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Sharded is the engine split K ways by key, driven from ONE goroutine. It has the method set the
// runtime drives an *Engine through and answers every one of them exactly as a single engine
// would: the same detections, the same return values and, above all, the same snapshot bytes. A
// K=4 build and a K=1 build (or one from before sharding existed) can each restore the other's
// snapshot, and K may change at any restart.
//
// Why that holds. An engine's state is a disjoint union over SeriesKeys, and a key's state changes
// in only three ways: a timer or pane close that the watermark made due, an event for that key, and
// a control operation addressed to that key. The watermark is the one thing every key shares, and
// every timer, pane close and lateness decision reads it. So the shards never keep a frontier of
// their own: EVERY shard is ticked with EVERY message's (sequence, time), including the messages
// that carry none of its keys, and the watermark is therefore the same function of the same prefix
// in each. Each key then sees exactly the operations, in exactly the order, it sees in a single
// engine. Only the interleaving of detections ACROSS keys differs, and nothing relies on that.
//
// How operations are routed:
//
//   - a rule event goes to the shard that owns its SeriesKey: the engine's own key, (rule, device),
//     or (rule, anchor) for Correlation, whose members are devices under one anchor. Routing by the
//     member would split an anchor's membership across shards;
//   - the per-key control operations (Descope, SetExpected, RemoveExpected and ClearRaised) go to the
//     owning shard alone. ClearRaised is the publish path's latch clear, and it must reach the
//     shard that holds the latch. RemoveExpected resolves at the watermark, which is the shared one;
//   - rule upserts and removals, tenant purges, Skip and Advance go to every shard, in order;
//   - queries are the OR, the sum, the union, or the single shared value, as each one means.
//
// Sharded is synchronous. It starts no goroutines and takes no locks, so it is exactly as safe to
// call as an Engine is: from one goroutine at a time.
type Sharded struct {
	shards   []*Engine
	lateness time.Duration
	scratch  [][]Event // per-shard event routing buffers, reused across messages
}

// maxShards bounds K. The limit is a sanity check on a configured value, not a design ceiling.
const maxShards = 64

// NewSharded builds an empty K-way engine. K is clamped to [1, maxShards]. allowedLateness has the
// meaning it has for NewEngine and is the same in every shard.
func NewSharded(k int, rules []Rule, allowedLateness time.Duration) *Sharded {
	k = clampShards(k)
	s := &Sharded{lateness: allowedLateness, shards: make([]*Engine, k), scratch: make([][]Event, k)}
	for i := range s.shards {
		s.shards[i] = NewEngine(rules, allowedLateness)
	}
	return s
}

func clampShards(k int) int {
	switch {
	case k < 1:
		return 1
	case k > maxShards:
		return maxShards
	}
	return k
}

// Shards reports K.
func (s *Sharded) Shards() int { return len(s.shards) }

// shardOf is the owner of a (rule, series). It is an FNV-1a over the two strings with a separator.
// The hash need only be stable within a process: the snapshot is canonical and carries no shard
// assignment, so a different hash or a different K simply re-splits it on restore.
func shardOf(rule, series string, k int) int {
	if k == 1 {
		return 0
	}
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(rule); i++ {
		h = (h ^ uint32(rule[i])) * prime
	}
	h *= prime // the separator byte, 0
	for i := 0; i < len(series); i++ {
		h = (h ^ uint32(series[i])) * prime
	}
	return int(h & 0x7fffffff % uint32(k))
}

func (s *Sharded) owner(key SeriesKey) *Engine {
	return s.shards[shardOf(key.Rule, key.Series, len(s.shards))]
}

// UpsertRule installs the rule in every shard.
func (s *Sharded) UpsertRule(r Rule) {
	for _, e := range s.shards {
		e.UpsertRule(r)
	}
}

// RemoveRule evicts the rule and all of its keyed state from every shard.
func (s *Sharded) RemoveRule(id string) {
	for _, e := range s.shards {
		e.RemoveRule(id)
	}
}

// RemoveMatching evicts every selected rule from every shard and reports how many entries went,
// as a single engine would. The rule set is replicated, so each removed rule is counted once per
// shard by the shards themselves; the replicas beyond the first are taken back off, or a purge
// would report K times the rules it removed. The state entries are disjoint across shards and are
// summed as they are.
func (s *Sharded) RemoveMatching(match func(ruleID string) bool) int {
	if len(s.shards) == 1 {
		return s.shards[0].RemoveMatching(match)
	}
	rules := 0
	for id := range s.shards[0].rules {
		if match(id) {
			rules++
		}
	}
	n := 0
	for _, e := range s.shards {
		n += e.RemoveMatching(match)
	}
	return n - (len(s.shards)-1)*rules
}

// Descope applies a membership flip in the shard that owns the key.
func (s *Sharded) Descope(ruleID, series string, at time.Time) bool {
	return s.shards[shardOf(ruleID, series, len(s.shards))].Descope(ruleID, series, at)
}

// ProcessEvent applies one event, with the same idempotency guard as the engine's.
func (s *Sharded) ProcessEvent(ev Event) { s.ProcessResolved(ev.Seq, ev.Time, []Event{ev}) }

// ProcessResolved ticks every shard with the message's (seq, t) and hands each shard the events it
// owns. The tick to a shard with no events is not an optimisation to skip: it is what keeps its
// watermark and sequence equal to every other shard's.
func (s *Sharded) ProcessResolved(seq uint64, t time.Time, evs []Event) {
	if len(s.shards) == 1 {
		s.shards[0].ProcessResolved(seq, t, evs)
		return
	}
	for i := range s.scratch {
		s.scratch[i] = s.scratch[i][:0]
	}
	k := len(s.shards)
	for i := range evs {
		o := shardOf(evs[i].Key.Rule, evs[i].Key.Series, k)
		s.scratch[o] = append(s.scratch[o], evs[i])
	}
	for i, e := range s.shards {
		e.ProcessResolved(seq, t, s.scratch[i])
	}
}

// Skip records a poison message's sequence in every shard and reports whether it moved.
func (s *Sharded) Skip(seq uint64) bool {
	moved := false
	for _, e := range s.shards {
		moved = e.Skip(seq) || moved
	}
	return moved
}

// Advance moves every shard's logical time to w and reports whether any state changed. It reaches
// every shard, idle or not: a shard skipped here would hold an older frontier than the rest.
func (s *Sharded) Advance(w time.Time) bool {
	changed := false
	for _, e := range s.shards {
		changed = e.Advance(w) || changed
	}
	return changed
}

// SetExpected arms a dead-man in the shard that owns the series.
func (s *Sharded) SetExpected(key SeriesKey, since time.Time) { s.owner(key).SetExpected(key, since) }

// RemoveExpected disarms a dead-man in the shard that owns the series.
func (s *Sharded) RemoveExpected(key SeriesKey) { s.owner(key).RemoveExpected(key) }

// ClearRaised drops a raised latch in the shard that holds it.
func (s *Sharded) ClearRaised(key SeriesKey) { s.owner(key).ClearRaised(key) }

// Drain returns and clears the detections every shard emitted. Order is each shard's emission order
// in turn: per series it is the order a single engine would give, and across series it is not
// promised.
func (s *Sharded) Drain() []Detection {
	if len(s.shards) == 1 {
		return s.shards[0].Drain()
	}
	var out []Detection
	for _, e := range s.shards {
		out = append(out, e.Drain()...)
	}
	return out
}

// DrainLateSamples sums the shards' late-sample telemetry.
func (s *Sharded) DrainLateSamples() uint64 {
	var n uint64
	for _, e := range s.shards {
		n += e.DrainLateSamples()
	}
	return n
}

// Watermark is the shared frontier. Every shard holds the same one; Snapshot fails if they do not.
func (s *Sharded) Watermark() time.Time { return s.shards[0].Watermark() }

// LastSeq is the shared position. Every shard holds the same one; Snapshot fails if they do not.
func (s *Sharded) LastSeq() uint64 { return s.shards[0].LastSeq() }

// HasPendingWork reports whether any shard has a timer or pane close scheduled.
func (s *Sharded) HasPendingWork() bool {
	for _, e := range s.shards {
		if e.HasPendingWork() {
			return true
		}
	}
	return false
}

// PendingTimerCount sums the shards' pending-deadline heaps.
func (s *Sharded) PendingTimerCount() int {
	n := 0
	for _, e := range s.shards {
		n += e.PendingTimerCount()
	}
	return n
}

// LiveKeyCounts sums the shards' per-rule live-entry counts. Keys are disjoint across shards, so
// the sum is the single engine's count.
func (s *Sharded) LiveKeyCounts() map[string]int {
	return s.sumCounts((*Engine).LiveKeyCounts)
}

// RetainedSampleCounts sums the shards' per-rule retained-sample counts.
func (s *Sharded) RetainedSampleCounts() map[string]int {
	return s.sumCounts((*Engine).RetainedSampleCounts)
}

func (s *Sharded) sumCounts(count func(*Engine) map[string]int) map[string]int {
	if len(s.shards) == 1 {
		return count(s.shards[0])
	}
	out := make(map[string]int)
	for _, e := range s.shards {
		for rule, n := range count(e) {
			out[rule] += n
		}
	}
	return out
}

// ExpectedKeys is the union of the shards' dead-man-armed series.
func (s *Sharded) ExpectedKeys() []SeriesKey {
	var out []SeriesKey
	for _, e := range s.shards {
		out = append(out, e.ExpectedKeys()...)
	}
	return out
}

// HeartbeatAbsenceKeys is the union of the shards' heartbeat-armed absence series.
func (s *Sharded) HeartbeatAbsenceKeys() []SeriesKey {
	var out []SeriesKey
	for _, e := range s.shards {
		out = append(out, e.HeartbeatAbsenceKeys()...)
	}
	return out
}

// checkAligned fails if the shards disagree on the frontier or the position. They cannot, if every
// message and every control operation reaches every shard, so a mismatch is a defect in this type
// and the one thing a checkpoint must refuse to write.
func (s *Sharded) checkAligned() error {
	w, q := s.shards[0].Watermark(), s.shards[0].LastSeq()
	for i, e := range s.shards[1:] {
		if !e.Watermark().Equal(w) || e.LastSeq() != q {
			return fmt.Errorf("shard %d is at (watermark %s, seq %d) but shard 0 is at (%s, %d): refusing to snapshot a state no single engine could be in",
				i+1, e.Watermark().Format(time.RFC3339Nano), e.LastSeq(), w.Format(time.RFC3339Nano), q)
		}
	}
	return nil
}

// Snapshot merges the shards into the single engine's snapshot, byte for byte.
func (s *Sharded) Snapshot() ([]byte, error) {
	if len(s.shards) == 1 {
		return s.shards[0].Snapshot()
	}
	if err := s.checkAligned(); err != nil {
		return nil, err
	}
	m := s.shards[0].snapshotState()
	for _, e := range s.shards[1:] {
		x := e.snapshotState()
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
	// Each shard's sections are sorted already; the concatenation of K of them is not, and the
	// canonical order is a total order over (rule, series), so a sort restores it exactly.
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
	sortByRuleSeries(m.Raised, func(i int) (string, string) { return m.Raised[i].Rule, m.Raised[i].Series })
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

// RestoreSharded rebuilds a K-way engine from a snapshot written by an engine of ANY shard count,
// or by the plain Engine: it splits the canonical state by key owner and restores one engine from
// each part. Every part carries the whole snapshot's watermark and position.
func RestoreSharded(k int, rules []Rule, allowedLateness time.Duration, data []byte) (*Sharded, error) {
	k = clampShards(k)
	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	if k == 1 {
		return &Sharded{lateness: allowedLateness, shards: []*Engine{restoreState(rules, allowedLateness, snap)}, scratch: make([][]Event, 1)}, nil
	}
	parts := make([]snapshot, k)
	for i := range parts {
		parts[i] = snapshot{Watermark: snap.Watermark, LastSeq: snap.LastSeq}
	}
	splitBy(snap.Active, k, func(x snapRun) (string, string) { return x.Rule, x.Series }, func(i int, x snapRun) { parts[i].Active = append(parts[i].Active, x) })
	splitBy(snap.Breaks, k, func(x snapBreak) (string, string) { return x.Rule, x.Series }, func(i int, x snapBreak) { parts[i].Breaks = append(parts[i].Breaks, x) })
	splitBy(snap.Timers, k, func(x snapTimer) (string, string) { return x.Rule, x.Series }, func(i int, x snapTimer) { parts[i].Timers = append(parts[i].Timers, x) })
	splitBy(snap.Gens, k, func(x snapGen) (string, string) { return x.Rule, x.Series }, func(i int, x snapGen) { parts[i].Gens = append(parts[i].Gens, x) })
	splitBy(snap.Sliding, k, func(x snapSliding) (string, string) { return x.Rule, x.Series }, func(i int, x snapSliding) { parts[i].Sliding = append(parts[i].Sliding, x) })
	splitBy(snap.Panes, k, func(x snapPane) (string, string) { return x.Rule, x.Series }, func(i int, x snapPane) { parts[i].Panes = append(parts[i].Panes, x) })
	splitBy(snap.Deltas, k, func(x snapDelta) (string, string) { return x.Rule, x.Series }, func(i int, x snapDelta) { parts[i].Deltas = append(parts[i].Deltas, x) })
	splitBy(snap.Counts, k, func(x snapCount) (string, string) { return x.Rule, x.Series }, func(i int, x snapCount) { parts[i].Counts = append(parts[i].Counts, x) })
	splitBy(snap.Sessions, k, func(x snapSession) (string, string) { return x.Rule, x.Series }, func(i int, x snapSession) { parts[i].Sessions = append(parts[i].Sessions, x) })
	splitBy(snap.Slides, k, func(x snapSlide) (string, string) { return x.Rule, x.Series }, func(i int, x snapSlide) { parts[i].Slides = append(parts[i].Slides, x) })
	splitBy(snap.Corr, k, func(x snapCorr) (string, string) { return x.Rule, x.Series }, func(i int, x snapCorr) { parts[i].Corr = append(parts[i].Corr, x) })
	splitBy(snap.Expected, k, func(x snapExpected) (string, string) { return x.Rule, x.Series }, func(i int, x snapExpected) { parts[i].Expected = append(parts[i].Expected, x) })
	splitBy(snap.Raised, k, func(x snapRaised) (string, string) { return x.Rule, x.Series }, func(i int, x snapRaised) { parts[i].Raised = append(parts[i].Raised, x) })
	splitBy(snap.Presence, k, func(x snapPresence) (string, string) { return x.Rule, x.Series }, func(i int, x snapPresence) { parts[i].Presence = append(parts[i].Presence, x) })
	s := &Sharded{lateness: allowedLateness, shards: make([]*Engine, k), scratch: make([][]Event, k)}
	for i := range parts {
		s.shards[i] = restoreState(rules, allowedLateness, parts[i])
	}
	return s, nil
}

// splitBy hands each item to the part that owns its (rule, series).
func splitBy[T any](items []T, k int, key func(T) (string, string), put func(shard int, x T)) {
	for _, x := range items {
		rule, series := key(x)
		put(shardOf(rule, series, k), x)
	}
}
