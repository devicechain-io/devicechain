// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This file pins the one-timer-per-key wheel and the lean (deque-free) sliding window against
// the engine they replaced.
//
// 🔴 THE OLD ENGINE IS NOT IN THIS FILE, IT IS IN testdata/timers_windows_golden.json. The goldens
// were recorded by running THIS generator against the engine as it stood before the change (a
// wheel that pushed an entry per event, a window that always built both deques), and every run
// here must reproduce them exactly. A golden is a SHA-256 over the whole observable output of a
// seed: every detection (rule, series, kind, edge, At, Value, HasValue) in drain order, plus the
// full Engine.Snapshot() bytes at every checkpoint — so a change to a timer's deadline, to firing
// order, to a generation number, or to anything serialized moves the hash.
//
// Beyond the hashes, for the first goldenSnapshotSeeds seeds the golden carries the OLD engine's
// literal mid-run snapshot. TestTimersWindowsOldSnapshotRestores restores those bytes into the
// current engine and requires it to continue to the recorded tail hash: a snapshot written before
// this change restores, and continues, identically.
//
// Regenerating (only ever against a deliberately different engine, never to make a failure go
// away): TIMERS_GOLDEN_UPDATE=1 go test -run TestTimersWindowsGolden .

const (
	goldenSeeds         = 64
	goldenSnapshotSeeds = 4
	goldenFile          = "testdata/timers_windows_golden.json"
	goldenLateness      = 5 * time.Second
)

type goldenSeed struct {
	Seed     int64  `json:"seed"`
	Messages int    `json:"messages"`
	Events   int    `json:"events"`
	Dets     int    `json:"detections"`
	Fired    int    `json:"absenceSessionDurationFired"`
	Hash     string `json:"hash"`       // detections + snapshots over the whole run
	TailHash string `json:"tailHash"`   // the same, for the run restored from MidSnap at message Mid
	Mid      int    `json:"mid"`        // message index of the mid-run snapshot
	MidSnap  string `json:"midSnap"`    // gzip+hex of the mid snapshot (first goldenSnapshotSeeds only)
	MidHash  string `json:"midSnapSha"` // sha256 of the mid snapshot (every seed)
	// OldTail is, for the seeds that carry MidSnap, the tail hash the PRE-CHANGE engine recorded when
	// it continued from that snapshot. MidSnap is that engine's literal snapshot (it has no frontier
	// version), so restoring it must re-serialize byte for byte and continue to this hash; TailHash
	// is the current engine's own tail from its own mid snapshot.
	OldTail string `json:"oldTailHash,omitempty"`
}

type goldenDoc struct {
	Seeds []goldenSeed `json:"seeds"`
}

type diffMsg struct {
	seq  uint64
	t    time.Time
	evs  []Event
	adv  time.Time // non-zero: an idle advance instead of a message
	snap bool      // checkpoint a snapshot after this step
}

// diffRules returns the rule mix for a seed: every sliding aggregate, absence, session, duration,
// and a couple of the kinds that share the wheel by other routes.
func diffRules(rng *rand.Rand) []Rule {
	var rs []Rule
	aggs := []AggOp{AggAvg, AggSum, AggCount, AggMin, AggMax}
	ops := []CmpOp{GT, GE, LT, LE}
	for i, a := range aggs {
		for j := 0; j < 2; j++ {
			win := time.Duration(8+rng.Intn(40)) * time.Second
			var th float64
			switch a {
			case AggCount:
				th = float64(2 + rng.Intn(6))
			case AggSum:
				th = float64(20 + rng.Intn(120))
			default:
				th = float64(8 + rng.Intn(12))
			}
			rs = append(rs, Rule{ID: fmt.Sprintf("t/sl%d_%d", i, j), Kind: SlidingAgg, Window: win, Agg: a, Op: ops[rng.Intn(len(ops))], Thresh: th})
		}
	}
	rs = append(rs,
		Rule{ID: "t/abs_short", Kind: Absence, Timeout: time.Duration(3+rng.Intn(6)) * time.Second},
		Rule{ID: "t/abs_long", Kind: Absence, Timeout: time.Duration(20+rng.Intn(30)) * time.Second},
		Rule{ID: "t/sess_sum", Kind: Session, Gap: time.Duration(3+rng.Intn(4)) * time.Second, Agg: AggSum, Op: GT, Thresh: float64(30 + rng.Intn(40))},
		Rule{ID: "t/sess_cnt", Kind: Session, Gap: time.Duration(2+rng.Intn(3)) * time.Second, Agg: AggCount, Op: GE, Thresh: float64(3 + rng.Intn(4))},
		Rule{ID: "t/dur", Kind: Duration, Hold: time.Duration(4+rng.Intn(8)) * time.Second},
		Rule{ID: "t/tumble", Kind: Aggregate, Window: 10 * time.Second, Agg: AggAvg, Op: GT, Thresh: 12},
		Rule{ID: "t/delta", Kind: DeltaRate, Op: GT, Thresh: 6},
	)
	return rs
}

// diffMessages generates a deterministic input stream: devices reporting on irregular cadences
// with whole-second timestamps (so deadlines from different keys TIE), bounded-late reordering
// within the lateness budget, occasional reordering beyond it, duplicate values (so the deque
// comparisons see equal values), silent devices, bursts, idle advances, and checkpoints.
func diffMessages(rng *rand.Rand, rules []Rule) []diffMsg {
	devices := 3 + rng.Intn(10)
	n := 400 + rng.Intn(900)
	clock := 0
	var out []diffMsg
	seq := uint64(0)
	silent := map[int]int{} // device -> clock it resumes at
	for i := 0; i < n; i++ {
		clock += rng.Intn(3) // 0 ties the message with the previous one
		if rng.Intn(40) == 0 {
			clock += 20 + rng.Intn(60) // a quiet spell: absences and sessions come due
		}
		if rng.Intn(25) == 0 {
			d := rng.Intn(devices)
			silent[d] = clock + 10 + rng.Intn(80)
		}
		if rng.Intn(15) == 0 {
			out = append(out, diffMsg{adv: base.Add(time.Duration(clock+rng.Intn(4)) * time.Second), snap: rng.Intn(6) == 0})
			continue
		}
		seq++
		k := 1 + rng.Intn(4)
		var evs []Event
		for j := 0; j < k; j++ {
			d := rng.Intn(devices)
			if resume, ok := silent[d]; ok && clock < resume {
				continue
			}
			ts := clock
			switch r := rng.Intn(10); {
			case r < 3:
				ts -= rng.Intn(5) // within the 5s lateness budget
			case r == 3:
				ts -= 5 + rng.Intn(25) // beyond it
			}
			if ts < 0 {
				ts = 0
			}
			val := float64(rng.Intn(24)) // small integers: ties, and sums that are exact
			if rng.Intn(9) == 0 {
				val += 0.5
			}
			match := rng.Intn(5) != 0
			series := fmt.Sprintf("dev-%02d", d)
			for _, r := range rules {
				evs = append(evs, Event{
					Seq: seq, Key: SeriesKey{Rule: r.ID, Series: series}, Time: base.Add(time.Duration(ts) * time.Second),
					Value: val, HasValue: true, Match: match,
				})
			}
		}
		out = append(out, diffMsg{seq: seq, t: base.Add(time.Duration(clock) * time.Second), evs: evs, snap: i%37 == 36})
	}
	return out
}

// diffRun feeds msgs[from:] into e and returns the hash of everything observable: each drained
// detection in order and each checkpoint snapshot's bytes. If snapAt >= 0 it also returns the
// snapshot taken immediately BEFORE message snapAt.
type diffResult struct {
	hash    string
	dets    int
	fired   int
	events  int
	midSnap []byte
}

func diffRun(t testing.TB, e *Engine, msgs []diffMsg, from, snapAt int, restoreEvery int, rules []Rule) diffResult {
	t.Helper()
	h := sha256.New()
	var res diffResult
	emit := func(ds []Detection) {
		for _, d := range ds {
			res.dets++
			if d.Kind == Absence || d.Kind == Duration || d.Kind == Session {
				res.fired++
			}
			fmt.Fprintf(h, "D|%s|%s|%d|%d|%d|%x|%t\n", d.RuleID, d.Series, d.Kind, d.Edge, d.At.UnixNano(), mathFloat(d.Value), d.HasValue)
		}
	}
	checkpoint := func(label string) {
		b, err := e.Snapshot()
		if err != nil {
			t.Fatalf("%s: snapshot: %v", label, err)
		}
		h.Write([]byte("S|"))
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	for i := from; i < len(msgs); i++ {
		m := msgs[i]
		if i == snapAt {
			b, err := e.Snapshot()
			if err != nil {
				t.Fatalf("mid snapshot: %v", err)
			}
			res.midSnap = b
		}
		if !m.adv.IsZero() {
			e.Advance(m.adv)
		} else {
			res.events += len(m.evs)
			e.ProcessReceived(m.seq, m.t, Receipt{}, m.evs)
		}
		emit(e.Drain())
		if m.snap {
			checkpoint(fmt.Sprintf("msg %d", i))
		}
		if restoreEvery > 0 && i%restoreEvery == restoreEvery-1 {
			// A live engine and one restored from its own snapshot must be indistinguishable.
			b, err := e.Snapshot()
			if err != nil {
				t.Fatalf("restore-point snapshot: %v", err)
			}
			re, err := Restore(rules, goldenLateness, b)
			if err != nil {
				t.Fatalf("restore: %v", err)
			}
			e = re
		}
	}
	// Drain the future: everything armed fires, so the final state of every timer is observed.
	e.Advance(base.Add(24 * time.Hour))
	emit(e.Drain())
	checkpoint("final")
	res.hash = hex.EncodeToString(h.Sum(nil))
	return res
}

func mathFloat(v float64) uint64 { return math.Float64bits(v) }

func gz(b []byte) string {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return hex.EncodeToString(buf.Bytes())
}

func ungz(t testing.TB, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// computeSeed produces the golden record for one seed from the CURRENT engine.
func computeSeed(t testing.TB, seed int64) goldenSeed {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	rules := diffRules(rng)
	msgs := diffMessages(rng, rules)
	mid := len(msgs) / 2

	full := diffRun(t, NewEngine(rules, goldenLateness), msgs, 0, mid, 0, rules)
	if full.midSnap == nil {
		t.Fatalf("seed %d: no mid snapshot", seed)
	}
	// Self-consistency (true of old and new alike): restoring at every 17th message changes nothing.
	chaos := diffRun(t, NewEngine(rules, goldenLateness), msgs, 0, -1, 17, rules)
	if chaos.hash != full.hash {
		// The snapshots in the hash are taken from engines that were themselves restored, so a
		// divergence here is a restore that loses or invents state.
		t.Fatalf("seed %d: a run restored from its own snapshot every 17 messages diverges from the straight run", seed)
	}
	re, err := Restore(rules, goldenLateness, full.midSnap)
	if err != nil {
		t.Fatalf("seed %d: restore mid: %v", seed, err)
	}
	tail := diffRun(t, re, msgs, mid, -1, 0, rules)
	sum := sha256.Sum256(full.midSnap)
	g := goldenSeed{
		Seed: seed, Messages: len(msgs), Events: full.events, Dets: full.dets, Fired: full.fired,
		Hash: full.hash, TailHash: tail.hash, Mid: mid, MidHash: hex.EncodeToString(sum[:]),
	}
	if seed < goldenSnapshotSeeds {
		g.MidSnap = gz(full.midSnap)
	}
	return g
}

func loadGolden(t testing.TB) goldenDoc {
	t.Helper()
	b, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("read %s: %v (TIMERS_GOLDEN_UPDATE=1 records it)", goldenFile, err)
	}
	var d goldenDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// TestTimersWindowsGolden is the differential test: the current engine's whole observable output,
// over goldenSeeds randomized streams, equals what the pre-change engine produced.
func TestTimersWindowsGolden(t *testing.T) {
	if os.Getenv("TIMERS_GOLDEN_UPDATE") == "1" {
		var d goldenDoc
		for s := int64(0); s < goldenSeeds; s++ {
			d.Seeds = append(d.Seeds, computeSeed(t, s))
		}
		b, err := json.MarshalIndent(d, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("golden recorded")
	}
	d := loadGolden(t)
	if len(d.Seeds) != goldenSeeds {
		t.Fatalf("golden holds %d seeds, want %d", len(d.Seeds), goldenSeeds)
	}
	var events, dets, fired int
	for _, want := range d.Seeds {
		got := computeSeed(t, want.Seed)
		got.MidSnap, want.MidSnap = "", "" // compared by hash; the bytes are the restore test's input
		want.OldTail = ""                  // the restore test's expectation, not an output of this run
		if got != want {
			t.Fatalf("seed %d diverges from the recorded engine:\n got  %+v\n want %+v", want.Seed, got, want)
		}
		events += got.Events
		dets += got.Dets
		fired += got.Fired
	}
	// The generator must keep exercising what it claims to: a stream that fires nothing proves nothing.
	if fired < 1000 || dets < 5000 {
		t.Fatalf("generator went slack: %d events, %d detections, %d timer firings", events, dets, fired)
	}
	t.Logf("%d seeds, %d events, %d detections (%d timer firings) identical to the recorded engine", len(d.Seeds), events, dets, fired)
}

// TestTimersWindowsOldSnapshotRestores restores snapshots WRITTEN BY THE PRE-CHANGE ENGINE and
// requires the current one to (a) re-serialize them byte-for-byte and (b) continue to the tail
// the old engine produced.
func TestTimersWindowsOldSnapshotRestores(t *testing.T) {
	d := loadGolden(t)
	n := 0
	for _, g := range d.Seeds {
		if g.MidSnap == "" {
			continue
		}
		n++
		rng := rand.New(rand.NewSource(g.Seed))
		rules := diffRules(rng)
		msgs := diffMessages(rng, rules)
		old := ungz(t, g.MidSnap)
		e, err := Restore(rules, goldenLateness, old)
		if err != nil {
			t.Fatalf("seed %d: restore: %v", g.Seed, err)
		}
		again, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, old) {
			t.Fatalf("seed %d: restoring the old snapshot and re-snapshotting changes its bytes", g.Seed)
		}
		if got := diffRun(t, e, msgs, g.Mid, -1, 0, rules); got.hash != g.OldTail {
			t.Fatalf("seed %d: restored from the old snapshot, the tail diverges from the old engine's", g.Seed)
		}
	}
	if n != goldenSnapshotSeeds {
		t.Fatalf("golden carries %d old snapshots, want %d", n, goldenSnapshotSeeds)
	}
}
