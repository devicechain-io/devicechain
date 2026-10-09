// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/presence"
)

// This file is the differential harness for sharding the engine in-process: a seeded generator
// drives the plain Engine and a K-way sharded implementation through the SAME operations and
// requires them to agree at every step. It exists before the sharded engine does, so that the
// engine is written against an oracle that has already been shown to fail.
//
// What "agree" means is the whole point, and it is deliberately strict:
//
//   - the SAME detections after every operation (as a multiset, and in order for each series —
//     the order BETWEEN series is not promised, because nothing downstream relies on it);
//   - the SAME watermark, last sequence, pending-work answer, timer count and live-key counts;
//   - the SAME return value from every operation that reports one;
//   - the SAME canonical snapshot BYTES, not merely an equivalent state: the snapshot is the
//     engine's on-disk contract, so a K=4 build and a K=1 build (or one from before sharding
//     existed) must each be able to restore the other's.
//
// The engine's watermark is ONE global frontier that every timer, pane close and lateness
// decision reads (watermark.go). A sharded engine is exact only if every shard sees the same
// frontier at the same position in the stream, which means every shard must be ticked with every
// message's (sequence, time) — including the messages that carry none of its keys. The
// generator therefore leans on exactly the things that notice a shard whose frontier lags:
// timers that fire only when ANOTHER key's event moves the clock, bounded-late and
// store-and-forward samples, anchors whose members live under many devices, and control
// operations (descopes, dead-man arming, purges, publish-path latch clears) interleaved with
// the message stream.
//
// 🔴 TO PLUG A SHARDED ENGINE IN, replace shardedUnderTest. Nothing else in this file needs to
// change; a sharded engine that passes it produces the detections and the snapshot bytes the
// single engine does.

// detectEngine is the method set the runtime drives an engine through. *Engine satisfies it, and
// a sharded engine must too, so the processor, the dead-man armer and the registry keep their
// call sites.
type detectEngine interface {
	UpsertRule(r Rule)
	RemoveRule(id string)
	RemoveMatching(match func(ruleID string) bool) int
	Descope(ruleID, series string, at time.Time) bool
	ProcessResolved(seq uint64, t time.Time, evs []Event)
	Skip(seq uint64) bool
	Advance(w time.Time) bool
	SetExpected(key SeriesKey, since time.Time)
	RemoveExpected(key SeriesKey)
	ClearRaised(key SeriesKey)
	Drain() []Detection
	Snapshot() ([]byte, error)
	Watermark() time.Time
	LastSeq() uint64
	HasPendingWork() bool
	PendingTimerCount() int
	LiveKeyCounts() map[string]int
}

var _ detectEngine = (*Engine)(nil)

// engineBuilder constructs an engine fresh or from a snapshot. The restore leg is what proves a
// shard count can change across a restart: the snapshot a K-way engine wrote is restored by an
// engine of a different K (or by the plain Engine) and the run continues in lockstep.
type engineBuilder struct {
	name    string
	fresh   func(rules []Rule, lateness time.Duration) detectEngine
	restore func(rules []Rule, lateness time.Duration, snap []byte) (detectEngine, error)
}

func plainEngineBuilder() engineBuilder {
	return engineBuilder{
		name: "plain",
		fresh: func(rules []Rule, lateness time.Duration) detectEngine {
			return NewEngine(rules, lateness)
		},
		restore: func(rules []Rule, lateness time.Duration, snap []byte) (detectEngine, error) {
			return Restore(rules, lateness, snap)
		},
	}
}

// singleEngineAdapter is the trivially-correct stand-in used until the sharded engine exists: it
// accepts a shard count and routes everything to ONE engine. It is correct by construction and
// therefore proves only that the harness is green on a correct implementation — the sensitivity
// half is proven by running the same harness against deliberately broken K-way implementations
// (see the pull request that introduced this file).
func singleEngineAdapter(k int) engineBuilder {
	b := plainEngineBuilder()
	b.name = fmt.Sprintf("single-engine-adapter(K=%d)", k)
	return b
}

// shardedUnderTest is THE seam. Today it returns the single-engine adapter.
var shardedUnderTest = singleEngineAdapter

// --- the generator ---------------------------------------------------------------------------

// diffKinds lists every rule kind, in RuleKind order: the engine has 11, and the dead-man
// (SetExpected on a device that never reports) is a further mechanism on the Absence kind that is
// generated separately, which is where the design's "12 kinds" comes from. A kind added to the
// engine must be added here, and TestDifferentialKindListIsTheEngines fails until it is.
var diffKinds = []struct {
	kind RuleKind
	name string
}{
	{Threshold, "thr"}, {Absence, "abs"}, {Duration, "dur"}, {Repeating, "rep"},
	{Aggregate, "agg"}, {DeltaRate, "dlt"}, {CountWindow, "cnt"}, {Session, "ses"},
	{SlidingAgg, "sld"}, {Correlation, "cor"}, {Connectivity, "con"},
}

// The least raises and resolves, over all the seeds, that the coverage test accepts: for each
// kind (summed over both tenants), and for each individual rule, so that neither tenant's shape of
// a kind can quietly stop producing a falling edge.
const (
	diffMinEdgesPerKind = 5
	diffMinEdgesPerRule = 1
)

const (
	diffLateness   = 3 * time.Second
	diffDevices    = 30 // devices d0..d29 report; d30..d39 never do
	diffNeverSeen  = 10
	diffAnchors    = 5
	diffSnapEvery  = 7
	diffTenantA    = "ta"
	diffTenantB    = "tb"
	diffRulePrefix = "/p@1/"
)

// diffRuleDef returns the rule a (tenant, kind) pair runs, with parameters scaled to the
// generator's time axis (a message every ~1.5 s on average) so every kind both fires and
// resolves within a run. The tenant picks between two aggregate shapes so the two tenants'
// rules are not copies of one another.
func diffRuleDef(tenant string, kind RuleKind, name string) Rule {
	r := Rule{ID: tenant + diffRulePrefix + name, Kind: kind}
	a := tenant == diffTenantA
	switch kind {
	case Absence:
		r.Timeout = 30 * time.Second
	case Duration:
		r.Hold = 12 * time.Second
	case Repeating:
		r.Window, r.Count = 150*time.Second, 3
		if !a {
			r.Count = 2
		}
	case Aggregate:
		r.Window, r.Op, r.Thresh = 100*time.Second, GT, 45
		if a {
			r.Agg = AggAvg
		} else {
			r.Agg, r.Op, r.Thresh = AggSum, GE, 120
		}
	case DeltaRate:
		r.Op, r.Thresh, r.Rate = GT, 25, false
		if !a {
			r.Thresh, r.Rate = 0.4, true // per second: a device reports about once a minute
		}
	case CountWindow:
		r.Count, r.Agg, r.Op, r.Thresh = 4, AggSum, GT, 320
		if !a {
			r.Count, r.Agg, r.Op, r.Thresh = 2, AggAvg, GE, 90
		}
	case Session:
		r.Gap, r.Agg, r.Op, r.Thresh = 90*time.Second, AggCount, GE, 3
		if !a {
			r.Thresh = 2
		}
	case SlidingAgg:
		r.Window, r.Agg, r.Op, r.Thresh = 150*time.Second, AggMax, GT, 85
		if !a {
			r.Agg, r.Op, r.Thresh = AggAvg, GE, 80
		}
	case Correlation:
		r.Window, r.Count, r.MemberCap = 30*time.Second, 3, 12
	}
	return r
}

// diffRuleAlt is the same rule id with a DIFFERENT body, so UpsertRule takes its changed-body
// path (GC the old rule's keyed state, start clean).
func diffRuleAlt(r Rule) Rule {
	r.Timeout += 7 * time.Second
	r.Hold += 3 * time.Second
	r.Gap += 4 * time.Second
	r.Window += 5 * time.Second
	r.Thresh += 5
	return r
}

type diffDevice struct {
	match   bool
	session uint64
	up      bool
}

type diffGen struct {
	rng   *rand.Rand
	base  time.Time
	seq   uint64
	maxT  time.Time
	defs  []Rule          // every rule the run may ever have, in a fixed order
	live  map[string]Rule // the rules currently installed (id -> body)
	alt   map[string]bool // whether a rule is currently on its alternate body
	dev   [diffDevices + diffNeverSeen]*diffDevice
	stats diffStats
	// lastRaised is the set of keys the most recent step raised, which is what the simulated
	// publish path chooses its latch clears from.
	lastRaised []SeriesKey
}

// diffStats says what a run exercised. A harness that quietly generates nothing is green for the
// wrong reason, so the coverage test asserts on these.
type diffStats struct {
	messages, controls   int
	raised, resolved     map[RuleKind]int
	raisedBy, resolvedBy map[string]int // the same, by rule id (a rule is a tenant's kind)
	deadManFires         int            // an Absence detection for a device that never reported
	descopesApplied      int
	clearRaisedCalls     int
	lateSamples          int
	purges, changedBody  int
	advances, snapshots  int
	crashes, skips       int
	correlationAnchored  int // correlation events whose member set spans several anchors
	connectivityDemoted  int
	distinctDetectedKeys map[SeriesKey]struct{}
}

func newDiffGen(seed int64) *diffGen {
	g := &diffGen{
		rng:  rand.New(rand.NewSource(seed)),
		base: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		live: map[string]Rule{},
		alt:  map[string]bool{},
		stats: diffStats{
			raised: map[RuleKind]int{}, resolved: map[RuleKind]int{},
			raisedBy: map[string]int{}, resolvedBy: map[string]int{},
			distinctDetectedKeys: map[SeriesKey]struct{}{},
		},
	}
	g.maxT = g.base
	for _, tn := range []string{diffTenantA, diffTenantB} {
		for _, k := range diffKinds {
			r := diffRuleDef(tn, k.kind, k.name)
			g.defs = append(g.defs, r)
			g.live[r.ID] = r
		}
	}
	for i := range g.dev {
		g.dev[i] = &diffDevice{up: true, session: uint64(1 + g.rng.Intn(5))}
	}
	return g
}

func (g *diffGen) rulesNow() []Rule {
	out := make([]Rule, 0, len(g.live))
	for _, r := range g.live {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func diffTenantOf(device int) string {
	if device%2 == 0 {
		return diffTenantA
	}
	return diffTenantB
}

func (g *diffGen) dur(maxMs int) time.Duration {
	if maxMs <= 0 {
		return 0
	}
	return time.Duration(g.rng.Intn(maxMs)) * time.Millisecond
}

// nextTime picks the next message's event time. Mostly it creeps forward; sometimes it jumps far
// enough to fire timers and close panes for keys that have gone quiet; sometimes it arrives
// late, behind the frontier, by more than the engine's lateness.
func (g *diffGen) nextTime() time.Time {
	switch p := g.rng.Float64(); {
	case p < 0.04:
		g.maxT = g.maxT.Add(40*time.Second + g.dur(160_000))
		return g.maxT
	case p < 0.12:
		return g.maxT.Add(-g.dur(9_000)) // out of order; beyond lateness about two times in three
	default:
		g.maxT = g.maxT.Add(g.dur(3_000))
		return g.maxT
	}
}

// sample builds a device's next reading. A device's matching state is sticky, so Duration holds,
// sessions and sliding aggregates get runs long enough to complete rather than flickering.
func (g *diffGen) sample(d int) (float64, bool) {
	st := g.dev[d]
	if g.rng.Float64() < 0.15 {
		st.match = !st.match
	}
	if st.match {
		return 60 + g.rng.Float64()*40, true
	}
	return g.rng.Float64() * 40, false
}

func (g *diffGen) message() (descopes []diffDescope, t time.Time, evs []Event) {
	t = g.nextTime()
	d := g.rng.Intn(diffDevices)
	tn := diffTenantOf(d)
	dev := fmt.Sprintf("d%d", d)
	for _, def := range g.defs {
		if !strings.HasPrefix(def.ID, tn+"/") {
			continue
		}
		if _, ok := g.live[def.ID]; !ok {
			continue
		}
		if g.rng.Float64() < 0.25 {
			continue // this message does not feed this rule
		}
		// Store-and-forward: now and then a message carries samples well behind its own time.
		samples := 1
		spread := time.Duration(0)
		if g.rng.Float64() < 0.06 {
			samples, spread = 1+g.rng.Intn(3), 45*time.Second
		}
		for s := 0; s < samples; s++ {
			et := t
			if s > 0 || spread > 0 && g.rng.Float64() < 0.5 {
				et = t.Add(-g.dur(int(spread / time.Millisecond)))
				g.stats.lateSamples++
			}
			val, match := g.sample(d)
			switch def.Kind {
			case Correlation:
				// A device reports under SEVERAL anchors, so an anchor's members come from many devices.
				for _, a := range []int{d % diffAnchors, (d + 2) % diffAnchors} {
					evs = append(evs, Event{
						Key: SeriesKey{Rule: def.ID, Series: fmt.Sprintf("a%d", a)}, Time: et,
						Member: dev, Match: g.rng.Float64() < 0.85,
					})
					g.stats.correlationAnchored++
				}
			case Connectivity:
				st := g.dev[d]
				edge := &PresenceEdge{SessionId: st.session}
				switch p := g.rng.Float64(); {
				case p < 0.12:
					edge.Claim = presence.ClaimDemoted
					g.stats.connectivityDemoted++
				case p < 0.30 && st.session > 1:
					edge.Claim, edge.SessionId = presence.ClaimDisconnected, st.session-1 // stale echo
				case st.up:
					edge.Claim = presence.ClaimDisconnected
					st.up = false
				default:
					st.session++
					edge.SessionId, edge.Claim = st.session, presence.ClaimConnected
					st.up = true
				}
				evs = append(evs, Event{Key: SeriesKey{Rule: def.ID, Series: dev}, Time: et, Presence: edge})
			default:
				evs = append(evs, Event{
					Key: SeriesKey{Rule: def.ID, Series: dev}, Time: et,
					Value: val, HasValue: true, Match: match,
				})
			}
		}
	}
	// Group descope flips: occasionally the device is OUT of scope for a rule this message, and the
	// processor applies that BEFORE the engine advances (a departed series' timer must not fire).
	if g.rng.Float64() < 0.08 {
		for n := 1 + g.rng.Intn(2); n > 0; n-- {
			def := g.defs[g.rng.Intn(len(g.defs))]
			descopes = append(descopes, diffDescope{rule: def.ID, series: dev, at: t})
		}
	}
	return descopes, t, evs
}

type diffDescope struct {
	rule, series string
	at           time.Time
}

// --- the runner ------------------------------------------------------------------------------

// diffOp is one step applied identically to both engines; it returns a string of whatever the
// operation reported, which must also agree.
type diffOp struct {
	desc string
	do   func(e detectEngine) string
}

// diffCrash describes a restart: at operation `at`, both engines are replaced by engines
// restored from the OTHER's snapshot, the plain one by `ref` and the other by `restoreAs`.
type diffCrash struct {
	at        int
	restoreAs engineBuilder
}

func normDetections(in []Detection) []Detection {
	out := append([]Detection(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RuleID != out[j].RuleID {
			return out[i].RuleID < out[j].RuleID
		}
		return out[i].Series < out[j].Series
	})
	return out
}

func detectionsEqual(a, b []Detection) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.RuleID != y.RuleID || x.Series != y.Series || x.Kind != y.Kind || x.Edge != y.Edge ||
			!x.At.Equal(y.At) || x.Value != y.Value || x.HasValue != y.HasValue {
			return i, false
		}
	}
	return 0, true
}

func describeDets(ds []Detection) string {
	var sb strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&sb, "\n    %s/%s kind=%d edge=%d at=%s val=%v(%v)", d.RuleID, d.Series, d.Kind, d.Edge, d.At.Format("15:04:05.000"), d.Value, d.HasValue)
	}
	if sb.Len() == 0 {
		return " (none)"
	}
	return sb.String()
}

func firstDiff(a, b []byte) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	lo := i - 60
	if lo < 0 {
		lo = 0
	}
	cut := func(x []byte) string {
		hi := i + 60
		if hi > len(x) {
			hi = len(x)
		}
		if lo > hi {
			return ""
		}
		return string(x[lo:hi])
	}
	return fmt.Sprintf("first difference at byte %d (lens %d vs %d)\n    plain:   ...%s...\n    sharded: ...%s...", i, len(a), len(b), cut(a), cut(b))
}

// runDifferential drives `ref` and `other` through nOps seeded operations in lockstep and returns
// the first disagreement, or nil. crash, if non-nil, restarts both engines mid-run.
func runDifferential(seed int64, nOps int, ref, other engineBuilder, crash *diffCrash) (*diffStats, error) {
	g := newDiffGen(seed)
	a := ref.fresh(g.rulesNow(), diffLateness)
	b := other.fresh(g.rulesNow(), diffLateness)

	compare := func(i int, desc string, ra, rb string) error {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("seed %d op %d (%s): %s", seed, i, desc, fmt.Sprintf(format, args...))
		}
		if ra != rb {
			return fail("operation result differs: plain=%q sharded=%q", ra, rb)
		}
		da, db := normDetections(a.Drain()), normDetections(b.Drain())
		if at, ok := detectionsEqual(da, db); !ok {
			return fail("detections differ (first at %d)\n  plain:%s\n  sharded:%s", at, describeDets(da), describeDets(db))
		}
		g.lastRaised = g.lastRaised[:0]
		for _, d := range da {
			if d.Edge == EdgeRaised {
				g.lastRaised = append(g.lastRaised, SeriesKey{Rule: d.RuleID, Series: d.Series})
			}
			g.stats.distinctDetectedKeys[SeriesKey{Rule: d.RuleID, Series: d.Series}] = struct{}{}
			if d.Edge == EdgeRaised {
				g.stats.raised[d.Kind]++
				g.stats.raisedBy[d.RuleID]++
			} else {
				g.stats.resolved[d.Kind]++
				g.stats.resolvedBy[d.RuleID]++
			}
			if d.Kind == Absence && d.Edge == EdgeRaised && deviceIndex(d.Series) >= diffDevices {
				g.stats.deadManFires++
			}
		}
		if !a.Watermark().Equal(b.Watermark()) {
			return fail("watermark differs: plain=%s sharded=%s", a.Watermark(), b.Watermark())
		}
		if a.LastSeq() != b.LastSeq() {
			return fail("last seq differs: plain=%d sharded=%d", a.LastSeq(), b.LastSeq())
		}
		if a.HasPendingWork() != b.HasPendingWork() {
			return fail("HasPendingWork differs: plain=%v sharded=%v", a.HasPendingWork(), b.HasPendingWork())
		}
		if a.PendingTimerCount() != b.PendingTimerCount() {
			return fail("PendingTimerCount differs: plain=%d sharded=%d", a.PendingTimerCount(), b.PendingTimerCount())
		}
		if ka, kb := fmt.Sprint(a.LiveKeyCounts()), fmt.Sprint(b.LiveKeyCounts()); ka != kb {
			return fail("LiveKeyCounts differ:\n    plain:   %s\n    sharded: %s", ka, kb)
		}
		return nil
	}
	snapshots := func(i int, desc string) ([]byte, []byte, error) {
		sa, err := a.Snapshot()
		if err != nil {
			return nil, nil, fmt.Errorf("seed %d op %d: plain snapshot: %w", seed, i, err)
		}
		sb, err := b.Snapshot()
		if err != nil {
			return nil, nil, fmt.Errorf("seed %d op %d: sharded snapshot: %w", seed, i, err)
		}
		g.stats.snapshots++
		if !bytes.Equal(sa, sb) {
			return nil, nil, fmt.Errorf("seed %d op %d (%s): snapshot bytes differ: %s", seed, i, desc, firstDiff(sa, sb))
		}
		return sa, sb, nil
	}
	apply := func(i int, op diffOp) error {
		ra, rb := op.do(a), op.do(b)
		if ra == "true" && strings.HasPrefix(op.desc, "descope") {
			g.stats.descopesApplied++
		}
		return compare(i, op.desc, ra, rb)
	}

	for i := 0; i < nOps; i++ {
		if crash != nil && i == crash.at {
			sa, sb, err := snapshots(i, "before restart")
			if err != nil {
				return &g.stats, err
			}
			var rerr error
			// Cross-restore: the plain engine resumes from the SHARDED engine's snapshot and the
			// other from the plain engine's, the other at a different shard count.
			if a, rerr = ref.restore(g.rulesNow(), diffLateness, sb); rerr != nil {
				return &g.stats, fmt.Errorf("seed %d op %d: plain restore: %w", seed, i, rerr)
			}
			if b, rerr = crash.restoreAs.restore(g.rulesNow(), diffLateness, sa); rerr != nil {
				return &g.stats, fmt.Errorf("seed %d op %d: %s restore: %w", seed, i, crash.restoreAs.name, rerr)
			}
			g.stats.crashes++
			if _, _, err := snapshots(i, "after restart"); err != nil {
				return &g.stats, err
			}
		}

		var steps []diffOp
		switch p := g.rng.Float64(); {
		case p < 0.80:
			g.stats.messages++
			descopes, t, evs := g.message()
			g.seq++
			seq := g.seq
			for _, d := range descopes {
				d := d
				steps = append(steps, diffOp{desc: fmt.Sprintf("descope %s/%s", d.rule, d.series), do: func(e detectEngine) string {
					return fmt.Sprint(e.Descope(d.rule, d.series, d.at))
				}})
			}
			steps = append(steps, diffOp{desc: fmt.Sprintf("message seq=%d t=%s events=%d", seq, t.Format("15:04:05.000"), len(evs)), do: func(e detectEngine) string {
				e.ProcessResolved(seq, t, evs)
				return ""
			}})
		case p < 0.84:
			g.stats.advances++
			w := g.maxT.Add(g.dur(70_000))
			steps = append(steps, diffOp{desc: "advance " + w.Format("15:04:05.000"), do: func(e detectEngine) string { return fmt.Sprint(e.Advance(w)) }})
		case p < 0.87:
			g.stats.skips++
			g.seq++
			seq := g.seq
			steps = append(steps, diffOp{desc: fmt.Sprintf("skip %d", seq), do: func(e detectEngine) string { return fmt.Sprint(e.Skip(seq)) }})
		case p < 0.92:
			// Dead-man arming, including for devices that will never report.
			g.stats.controls++
			d := g.rng.Intn(diffDevices + diffNeverSeen)
			key := SeriesKey{Rule: diffTenantOf(d) + diffRulePrefix + "abs", Series: fmt.Sprintf("d%d", d)}
			since := g.maxT.Add(-g.dur(30_000))
			if g.rng.Float64() < 0.25 {
				steps = append(steps, diffOp{desc: "remove expected " + key.Series, do: func(e detectEngine) string { e.RemoveExpected(key); return "" }})
			} else {
				steps = append(steps, diffOp{desc: "set expected " + key.Series, do: func(e detectEngine) string { e.SetExpected(key, since); return "" }})
			}
		case p < 0.945:
			// Rule publish: a changed body for a live rule, or a re-install of any that are missing.
			g.stats.controls++
			def := g.defs[g.rng.Intn(len(g.defs))]
			next := def
			if g.alt[def.ID] = !g.alt[def.ID]; g.alt[def.ID] {
				next = diffRuleAlt(def)
			}
			g.live[def.ID] = next
			g.stats.changedBody++
			steps = append(steps, diffOp{desc: "upsert " + def.ID, do: func(e detectEngine) string { e.UpsertRule(next); return "" }})
		case p < 0.955:
			g.stats.controls++
			def := g.defs[g.rng.Intn(len(g.defs))]
			delete(g.live, def.ID)
			steps = append(steps, diffOp{desc: "remove rule " + def.ID, do: func(e detectEngine) string { e.RemoveRule(def.ID); return "" }})
		case p < 0.965:
			// Tenant purge: remove every rule under a prefix, state and all.
			g.stats.controls++
			g.stats.purges++
			prefix := diffTenantB + "/"
			for id := range g.live {
				if strings.HasPrefix(id, prefix) {
					delete(g.live, id)
				}
			}
			steps = append(steps, diffOp{desc: "purge " + prefix, do: func(e detectEngine) string {
				// Only "did it find anything" is the contract (the count is deliberately generous, and a
				// rule installed on every shard is counted on each of them), so only that is compared.
				return fmt.Sprint(e.RemoveMatching(func(id string) bool { return strings.HasPrefix(id, prefix) }) > 0)
			}})
		default:
			// Re-publish whatever is missing, so purged and removed rules come back clean.
			g.stats.controls++
			var missing []Rule
			for _, def := range g.defs {
				if _, ok := g.live[def.ID]; !ok {
					missing = append(missing, def)
					g.live[def.ID] = def
					g.alt[def.ID] = false
				}
			}
			steps = append(steps, diffOp{desc: fmt.Sprintf("republish %d rules", len(missing)), do: func(e detectEngine) string {
				for _, r := range missing {
					e.UpsertRule(r)
				}
				return ""
			}})
		}
		for _, st := range steps {
			if err := apply(i, st); err != nil {
				return &g.stats, err
			}
		}

		// The publish path's latch clear: after a message, some of what it raised is dropped as
		// superseded, and the engine is told so (dropSupersededDetections -> ClearRaised). It must
		// reach the engine that OWNS the key, or the latch stays set and the next raise is swallowed.
		if len(steps) > 0 && strings.HasPrefix(steps[len(steps)-1].desc, "message") {
			keys := append([]SeriesKey(nil), g.lastRaised...)
			for _, k := range keys {
				if g.rng.Float64() < 0.3 {
					k := k
					g.stats.clearRaisedCalls++
					if err := apply(i, diffOp{desc: fmt.Sprintf("clear raised %s/%s", k.Rule, k.Series), do: func(e detectEngine) string { e.ClearRaised(k); return "" }}); err != nil {
						return &g.stats, err
					}
				}
			}
		}

		if i%diffSnapEvery == 0 {
			if _, _, err := snapshots(i, "periodic"); err != nil {
				return &g.stats, err
			}
		}
	}
	_, _, err := snapshots(nOps, "final")
	return &g.stats, err
}

func deviceIndex(series string) int {
	var n int
	if _, err := fmt.Sscanf(series, "d%d", &n); err != nil {
		return -1
	}
	return n
}

// --- the tests -------------------------------------------------------------------------------

var (
	diffSeeds   = []int64{1, 2, 3, 4, 5, 6}
	diffOpCount = 800
	diffShardKs = []int{2, 3, 7, 16}
)

// TestShardedEngineIsTheEngine is the core differential: for every shard count, the engine under
// test must be indistinguishable from the plain engine across a seeded stream covering every rule
// kind, step by step, down to the snapshot bytes.
func TestShardedEngineIsTheEngine(t *testing.T) {
	for _, k := range diffShardKs {
		for _, seed := range diffSeeds {
			k, seed := k, seed
			t.Run(fmt.Sprintf("K=%d/seed=%d", k, seed), func(t *testing.T) {
				if _, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), shardedUnderTest(k), nil); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestShardedEngineRestoresAcrossShardCounts crashes mid-run and resumes each engine from the
// other's snapshot, the sharded one at a DIFFERENT shard count. The shard count is not part of
// the persisted state, so changing it must be a restart, not a migration.
func TestShardedEngineRestoresAcrossShardCounts(t *testing.T) {
	restartAs := map[int]int{2: 5, 3: 16, 7: 2, 16: 3}
	for _, k := range diffShardKs {
		for _, seed := range diffSeeds {
			k, seed := k, seed
			t.Run(fmt.Sprintf("K=%d-to-K=%d/seed=%d", k, restartAs[k], seed), func(t *testing.T) {
				at := diffOpCount/4 + int(seed*37)%(diffOpCount/2) // a different restart point per seed
				_, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), shardedUnderTest(k),
					&diffCrash{at: at, restoreAs: shardedUnderTest(restartAs[k])})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestShardedEngineRestoresIntoPlainEngine is the rollback direction: what a sharded build wrote,
// the plain engine reads and continues from.
func TestShardedEngineRestoresIntoPlainEngine(t *testing.T) {
	for _, seed := range diffSeeds {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			_, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), shardedUnderTest(4),
				&diffCrash{at: diffOpCount / 2, restoreAs: plainEngineBuilder()})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestDifferentialGeneratorExercisesEveryKind guards the harness itself: a differential that
// generates nothing for some kind is green for that kind for the wrong reason. Over the seeds it
// requires a raise and a resolve for every rule kind (except Connectivity's, which resolves on
// a reconnect and a demotion both), a dead-man firing for a device that never reported,
// descopes that changed state, publish-path latch clears, store-and-forward samples, changed-body
// upserts, a purge, and a spread of detected keys.
func TestDifferentialGeneratorExercisesEveryKind(t *testing.T) {
	total := diffStats{raised: map[RuleKind]int{}, resolved: map[RuleKind]int{}, raisedBy: map[string]int{}, resolvedBy: map[string]int{}, distinctDetectedKeys: map[SeriesKey]struct{}{}}
	descoped := 0
	for _, seed := range diffSeeds {
		st, err := runDifferential(seed, diffOpCount, plainEngineBuilder(), plainEngineBuilder(), nil)
		if err != nil {
			t.Fatalf("plain engine disagrees with itself: %v", err)
		}
		for k, n := range st.raised {
			total.raised[k] += n
		}
		for k, n := range st.resolved {
			total.resolved[k] += n
		}
		for k, n := range st.raisedBy {
			total.raisedBy[k] += n
		}
		for k, n := range st.resolvedBy {
			total.resolvedBy[k] += n
		}
		for k := range st.distinctDetectedKeys {
			total.distinctDetectedKeys[k] = struct{}{}
		}
		total.deadManFires += st.deadManFires
		total.clearRaisedCalls += st.clearRaisedCalls
		total.lateSamples += st.lateSamples
		total.purges += st.purges
		total.changedBody += st.changedBody
		total.advances += st.advances
		total.skips += st.skips
		total.correlationAnchored += st.correlationAnchored
		total.connectivityDemoted += st.connectivityDemoted
		descoped += st.descopesApplied
	}
	for _, k := range diffKinds {
		if n := total.raised[k.kind]; n < diffMinEdgesPerKind {
			t.Errorf("kind %s raised %d times across %d seeds (want at least %d): the differential barely exercises it", k.name, n, len(diffSeeds), diffMinEdgesPerKind)
		}
		if n := total.resolved[k.kind]; n < diffMinEdgesPerKind {
			t.Errorf("kind %s resolved %d times across %d seeds (want at least %d): its falling edge is barely exercised", k.name, n, len(diffSeeds), diffMinEdgesPerKind)
		}
	}
	// And each RULE (a tenant's kind, so both aggregate shapes are held to it) at least once.
	for _, def := range newDiffGen(0).defs {
		if n := total.raisedBy[def.ID]; n < diffMinEdgesPerRule {
			t.Errorf("rule %s raised %d times across %d seeds (want at least %d): the differential barely exercises it", def.ID, n, len(diffSeeds), diffMinEdgesPerRule)
		}
		if n := total.resolvedBy[def.ID]; n < diffMinEdgesPerRule {
			t.Errorf("rule %s resolved %d times across %d seeds (want at least %d): its falling edge is barely exercised", def.ID, n, len(diffSeeds), diffMinEdgesPerRule)
		}
	}
	for name, n := range map[string]int{
		"dead-man fires for never-seen devices": total.deadManFires,
		"publish-path latch clears":             total.clearRaisedCalls,
		"store-and-forward samples":             total.lateSamples,
		"purges":                                total.purges,
		"changed-body upserts":                  total.changedBody,
		"idle advances":                         total.advances,
		"poison skips":                          total.skips,
		"multi-anchor correlation events":       total.correlationAnchored,
		"connectivity demotions":                total.connectivityDemoted,
		"descopes that changed state":           descoped,
	} {
		if n == 0 {
			t.Errorf("no %s were generated", name)
		}
	}
	if n := len(total.distinctDetectedKeys); n < 100 {
		t.Errorf("only %d distinct (rule, series) keys detected; a sharded run needs far more than shards to be meaningful", n)
	}
}

// TestDifferentialKindListIsTheEngines keeps diffKinds the engine's kinds, in order. A kind
// dropped from the list is a kind no rule is built for, so it would be green for the wrong reason.
func TestDifferentialKindListIsTheEngines(t *testing.T) {
	if len(diffKinds) != int(Connectivity)+1 {
		t.Fatalf("diffKinds lists %d kinds; the engine has %d (Connectivity is the last)", len(diffKinds), int(Connectivity)+1)
	}
	for i, k := range diffKinds {
		if k.kind != RuleKind(i) {
			t.Errorf("diffKinds[%d] is %s (%d); position %d is kind %d", i, k.name, k.kind, i, i)
		}
	}
}
