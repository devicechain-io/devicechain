// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// The positive control certifies the oracle only when it REACHED the target AND
// the full production reconcile passes on intact truth — an equal count that
// never converged, a short read, or a baseline that is itself inconclusive (a
// dirty drive, an unmet floor) must not certify the oracle.
func TestPositiveControlHeld(t *testing.T) {
	const floor int64 = 50
	cases := []struct {
		name              string
		accept, persisted int64
		failed            int64
		reached, want     bool
	}{
		{"reached, equal, clean", 1000, 1000, 0, true, true},
		{"equal but not reached", 1000, 1000, 0, false, false},
		{"reached but short", 1000, 999, 0, true, false},
		{"reached but over", 1000, 1001, 0, true, false},
		{"equal but dirty drive — baseline inconclusive", 1000, 1000, 4, true, false},
		{"equal but below load floor — baseline inconclusive", 40, 40, 0, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := positiveControlHeld(c.accept, c.persisted, c.failed, floor, c.reached); got != c.want {
				t.Fatalf("positiveControlHeld(acc=%d per=%d fail=%d reached=%v)=%v want %v",
					c.accept, c.persisted, c.failed, c.reached, got, c.want)
			}
		})
	}
}

var (
	keyA = IdentityKey{Device: "dev-1", OccurredMicros: 1_790_000_000_000_001}
	keyB = IdentityKey{Device: "dev-2", OccurredMicros: 1_790_000_000_000_002}
)

// dropFound is the identity report of a store missing exactly key.
func dropFound(key IdentityKey) IdentityReport {
	return IdentityReport{Missing: 1, Samples: IdentitySamples{Missing: []string{key.String()}}}
}

// swapFound is the identity report of a store missing exactly missing and holding dup twice.
func swapFound(missing, dup IdentityKey) IdentityReport {
	return IdentityReport{Missing: 1, DuplicateKeys: 1, ExtraCopies: 1,
		Samples: IdentitySamples{Missing: []string{missing.String()}, Duplicate: []string{dup.String() + " x2"}}}
}

// The negative control passes ONLY when the production Await timed out BELOW the
// target (reached=false — the path a real drop reports through), the settled count
// is exactly accepted-1, the production completeness invariant fails as a drop, AND
// identity names exactly the deleted event missing. This is the anti–check-that-
// cannot-fail proof: the self-test must not pass by Await spuriously reaching, by the
// oracle going inconclusive for the wrong reason, by a mis-sized deletion, nor by
// identity failing on some other event. The accepted/intact columns are varied
// independently so the two roles cannot be conflated.
func TestNegativeControlDetected(t *testing.T) {
	const floor int64 = 50
	good := dropFound(keyA)
	cases := []struct {
		name                    string
		accepted, intact, after int64
		failed                  int64
		reached                 bool
		id                      IdentityReport
		want                    bool
		why                     string
	}{
		{"deleted one, timed out below target, completeness fails", 1000, 1000, 999, 0, false, good, true,
			"the designed detection"},
		{"Await spuriously reached the target", 1000, 1000, 999, 0, true, good, false,
			"reached=true is a broken quiesce, not a detected drop — the exact production false-PASS to catch"},
		{"nothing deleted — count unchanged", 1000, 1000, 1000, 0, false, good, false,
			"no drop; and 1000 != intact-1"},
		{"two deleted — ambiguous stimulus", 1000, 1000, 998, 0, false, good, false,
			"must be exactly accepted-1"},
		{"one short but drive was dirty — inconclusive", 1000, 1000, 999, 3, false, good, false,
			"failed>0 makes completeness inconclusive, not a detected drop"},
		{"one short but below load floor — inconclusive", 40, 40, 39, 0, false, good, false,
			"accepted<floor: completeness reports inconclusive, not a drop"},
		{"accepted != intact — contaminated baseline", 1000, 1001, 1000, 0, false, good, false,
			"after==intact-1 (1000==1000) but Reconcile on accepted sees persisted==accepted → completeness PASSES"},
		{"identity reconciled", 1000, 1000, 999, 0, false, IdentityReport{Reconciled: true}, false,
			"identity did not see the deletion"},
		{"identity names another event", 1000, 1000, 999, 0, false, dropFound(keyB), false,
			"the missing event must be the one deleted"},
		{"identity also reports a duplicate", 1000, 1000, 999, 0, false, swapFound(keyA, keyB), false,
			"nothing was duplicated"},
		{"identity also reports an unexpected row", 1000, 1000, 999, 0, false,
			func() IdentityReport { r := dropFound(keyA); r.Unexpected = 1; return r }(), false,
			"nothing was added"},
		{"identity inconclusive", 1000, 1000, 999, 0, false,
			func() IdentityReport { r := dropFound(keyA); r.Inconclusive = []string{"x"}; return r }(), false,
			"an inconclusive identity verdict detects nothing"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := negativeControlDetected(c.accepted, c.intact, c.after, c.failed, floor, c.reached, c.id, keyA)
			if got != c.want {
				t.Fatalf("negativeControlDetected(acc=%d int=%d after=%d failed=%d reached=%v)=%v want %v (%s)",
					c.accepted, c.intact, c.after, c.failed, c.reached, got, c.want, c.why)
			}
		})
	}
}

// The swap control is the issue's case made live: one event lost and another stored
// twice, so the totals agree. It passes only when the COUNT passes — reached, exactly
// accepted, every count invariant green, which is the proof the count is blind to it —
// AND identity names exactly the deleted event missing and the duplicated one stored
// twice. Without the count half, a failing identity check would prove nothing about
// what it adds; without the identity half, the check is the count again.
func TestSwapControlDetected(t *testing.T) {
	const floor int64 = 50
	good := swapFound(keyA, keyB)
	cases := []struct {
		name            string
		accepted, after int64
		failed          int64
		reached         bool
		id              IdentityReport
		want            bool
	}{
		{"count blind, identity names both", 1000, 1000, 0, true, good, true},
		{"count did not reach — not shown blind", 1000, 999, 0, false, good, false},
		{"count over — not shown blind", 1000, 1001, 0, true, good, false},
		{"dirty drive — count reconcile does not pass", 1000, 1000, 2, true, good, false},
		{"below the floor — count reconcile does not pass", 40, 40, 0, true, good, false},
		{"identity saw only the missing event", 1000, 1000, 0, true, dropFound(keyA), false},
		{"identity clean", 1000, 1000, 0, true, IdentityReport{Reconciled: true}, false},
		{"identity names the events the wrong way round", 1000, 1000, 0, true, swapFound(keyB, keyA), false},
		{"identity also reports an unexpected row", 1000, 1000, 0, true,
			func() IdentityReport { r := swapFound(keyA, keyB); r.Unexpected = 1; return r }(), false},
		{"identity sees the duplicate stored three times", 1000, 1000, 0, true,
			func() IdentityReport { r := swapFound(keyA, keyB); r.ExtraCopies = 2; return r }(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := swapControlDetected(c.accepted, c.after, c.failed, floor, c.reached, c.id, keyA, keyB); got != c.want {
				t.Fatalf("swapControlDetected = %v, want %v", got, c.want)
			}
		})
	}
}

// storeFake is an event store in memory: the counter, the identity reader and the
// perturber all act on the same rows, so a control's stimulus reaches both of the
// oracle's reads the way it does live.
type storeFake struct {
	mu   sync.Mutex
	rows map[string][]int64

	deleteRows, dupRows int
	deleteCalled        bool
	dupCalled           bool
}

func (s *storeFake) Count(_ context.Context, _ Window) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, r := range s.rows {
		n += int64(len(r))
	}
	return n, nil
}

func (s *storeFake) DeviceIdentities(_ context.Context, device string, _ Window) ([]int64, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.rows[device]...), 1, nil
}

// keys lists every stored row's identity in (time, device) order.
func (s *storeFake) keys() []IdentityKey {
	var out []IdentityKey
	for d, r := range s.rows {
		for _, us := range r {
			out = append(out, IdentityKey{Device: d, OccurredMicros: us})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurredMicros != out[j].OccurredMicros {
			return out[i].OccurredMicros < out[j].OccurredMicros
		}
		return out[i].Device < out[j].Device
	})
	return out
}

func (s *storeFake) remove(k IdentityKey) {
	r := s.rows[k.Device]
	for i, us := range r {
		if us == k.OccurredMicros {
			s.rows[k.Device] = append(r[:i:i], r[i+1:]...)
			return
		}
	}
}

// DeleteOneMeasurement removes the earliest deleteRows rows and names the first.
func (s *storeFake) DeleteOneMeasurement(_ context.Context, _ Window) (IdentityKey, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalled = true
	keys := s.keys()
	if s.deleteRows == 0 || len(keys) == 0 {
		return IdentityKey{}, 0, nil
	}
	for _, k := range keys[:s.deleteRows] {
		s.remove(k)
	}
	return keys[0], s.deleteRows, nil
}

// DuplicateOneMeasurement stores dupRows more copies of the latest row and names it.
func (s *storeFake) DuplicateOneMeasurement(_ context.Context, _ Window) (IdentityKey, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dupCalled = true
	keys := s.keys()
	if s.dupRows == 0 || len(keys) == 0 {
		return IdentityKey{}, 0, nil
	}
	k := keys[len(keys)-1]
	for i := 0; i < s.dupRows; i++ {
		s.rows[k.Device] = append(s.rows[k.Device], k.OccurredMicros)
	}
	return k, s.dupRows, nil
}

// selfTestFixture is a clean drive of devices × perDevice accepted Measurements, every
// one stored once, with a perturber that deletes one and duplicates one.
func selfTestFixture(t *testing.T, devices, perDevice int) (controlInputs, *storeFake) {
	t.Helper()
	led := sim.NewIdentityLedger()
	store := &storeFake{rows: map[string][]int64{}, deleteRows: 1, dupRows: 1}
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	var tokens []string
	for d := 0; d < devices; d++ {
		tok := fmt.Sprintf("dev-%03d", d)
		tokens = append(tokens, tok)
		for i := 0; i < perDevice; i++ {
			at := base.Add(time.Duration(i)*time.Second + time.Duration(d)*time.Microsecond)
			led.Record(sim.OutcomeAccepted, tok, "Measurement", at.Format(time.RFC3339Nano))
			store.rows[tok] = append(store.rows[tok], at.UnixMicro())
		}
	}
	accepted := int64(devices * perDevice)
	return controlInputs{
		counter:        store,
		reader:         store,
		perturber:      store,
		ledger:         led,
		devices:        tokens,
		snap:           sim.Snapshot{Emitted: accepted},
		driveEnd:       time.Now(),
		minAccepted:    50,
		poll:           time.Millisecond,
		quiesceTimeout: 40 * time.Millisecond,
	}, store
}

// evaluateControls' load-bearing sequencing has no live coverage otherwise: a
// failed positive control must short-circuit WITHOUT ever invoking the perturber
// (or the self-test would perturb against an unproven baseline), and a mis-sized
// perturbation must go inconclusive without a verdict. Driven through an in-memory
// store the counter, the identity reader and the perturber share.
func TestEvaluateControls(t *testing.T) {
	ctx := context.Background()

	t.Run("sound: intact, then a detected drop, then a detected swap", func(t *testing.T) {
		in, store := selfTestFixture(t, 4, 15)
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.inconclusive {
			t.Fatalf("unexpected inconclusive: %s", out.reason)
		}
		if !out.positive.Passed || !out.negative.Passed || !out.swap.Passed {
			t.Fatalf("all three controls should pass: positive=%v (%s) negative=%v (%s) swap=%v (%s)",
				out.positive.Passed, out.positive.Detail, out.negative.Passed, out.negative.Detail, out.swap.Passed, out.swap.Detail)
		}
		if out.persistedIntact != 60 || out.persistedPerturbed != 59 || out.persistedSwapped != 60 {
			t.Fatalf("intact=%d perturbed=%d swapped=%d; want 60/59/60", out.persistedIntact, out.persistedPerturbed, out.persistedSwapped)
		}
		if !store.deleteCalled || !store.dupCalled || out.deleted != 1 || out.duplicated != 1 {
			t.Fatalf("perturber delete=%v (%d) dup=%v (%d)", store.deleteCalled, out.deleted, store.dupCalled, out.duplicated)
		}
		base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
		wantDeleted := IdentityKey{Device: "dev-000", OccurredMicros: base.UnixMicro()}.String()
		wantDup := IdentityKey{Device: "dev-003", OccurredMicros: base.Add(14*time.Second + 3*time.Microsecond).UnixMicro()}.String()
		if out.deletedKey != wantDeleted || out.duplicatedKey != wantDup {
			t.Fatalf("deleted %q duplicated %q; want %q and %q", out.deletedKey, out.duplicatedKey, wantDeleted, wantDup)
		}
		if !out.identityIntact.Reconciled {
			t.Errorf("intact identity did not reconcile: %+v", out.identityIntact)
		}
		if got := out.identityAfterSwap.Samples.Duplicate; len(got) != 1 || got[0] != wantDup+" x2" {
			t.Errorf("after the swap, duplicate samples = %v; want [%s x2]", got, wantDup)
		}
	})

	t.Run("positive control fails on the count: perturber never invoked", func(t *testing.T) {
		in, store := selfTestFixture(t, 4, 15)
		store.rows["dev-001"] = store.rows["dev-001"][1:] // never reaches 60
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if store.deleteCalled || store.dupCalled {
			t.Fatal("perturber must NOT run when the baseline is unproven — it would perturb a dirty baseline")
		}
		if !out.inconclusive || out.positive.Passed {
			t.Fatalf("want inconclusive with failed positive; got inconclusive=%v positive=%v", out.inconclusive, out.positive.Passed)
		}
	})

	t.Run("positive control fails on identity alone: perturber never invoked", func(t *testing.T) {
		// The totals agree (60 = 60), but one stored row is not the event that was sent:
		// a count-only positive control would certify this baseline.
		in, store := selfTestFixture(t, 4, 15)
		store.rows["dev-002"][3] += 7
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.persistedIntact != 60 {
			t.Fatalf("persistedIntact = %d; the count should agree for this case", out.persistedIntact)
		}
		if store.deleteCalled || !out.inconclusive || out.positive.Passed {
			t.Fatalf("want inconclusive, positive failed, perturber untouched; got inconclusive=%v positive=%v delete=%v",
				out.inconclusive, out.positive.Passed, store.deleteCalled)
		}
		if out.identityIntact.Missing != 1 || out.identityIntact.Unexpected != 1 {
			t.Errorf("intact identity = missing %d unexpected %d; want 1 and 1", out.identityIntact.Missing, out.identityIntact.Unexpected)
		}
	})

	t.Run("mis-sized deletion: inconclusive, no negative verdict, no swap", func(t *testing.T) {
		in, store := selfTestFixture(t, 4, 15)
		store.deleteRows = 0 // perturber found nothing to delete
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if !store.deleteCalled || out.deleted != 0 || store.dupCalled {
			t.Fatalf("delete called=%v deleted=%d dup called=%v", store.deleteCalled, out.deleted, store.dupCalled)
		}
		if !out.inconclusive || out.negative.Passed || out.swap.Passed {
			t.Fatalf("want inconclusive with no passed verdict; got inconclusive=%v negative=%v swap=%v",
				out.inconclusive, out.negative.Passed, out.swap.Passed)
		}
	})

	t.Run("over-deletion: the negative control does not pass", func(t *testing.T) {
		in, store := selfTestFixture(t, 4, 15)
		store.deleteRows = 2
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if !out.inconclusive || out.negative.Passed {
			t.Fatalf("two rows deleted: want inconclusive and no negative pass; got inconclusive=%v negative=%v", out.inconclusive, out.negative.Passed)
		}
	})

	t.Run("mis-sized duplication: inconclusive, no swap verdict", func(t *testing.T) {
		in, store := selfTestFixture(t, 4, 15)
		store.dupRows = 0
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if !out.negative.Passed {
			t.Fatalf("the negative control should still have been decided and passed: %s", out.negative.Detail)
		}
		if !store.dupCalled || !out.inconclusive || out.swap.Passed {
			t.Fatalf("want inconclusive with no swap pass; got dup called=%v inconclusive=%v swap=%v", store.dupCalled, out.inconclusive, out.swap.Passed)
		}
	})

	t.Run("two extra copies are not the stimulus", func(t *testing.T) {
		// Two inserted rows would make the count overshoot, so the count could not be
		// shown blind: the stimulus is mis-sized and the swap control must not pass.
		in, store := selfTestFixture(t, 4, 15)
		store.dupRows = 2
		out, err := evaluateControls(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.swap.Passed {
			t.Fatalf("swap control passed on an overshooting count: %s", out.swap.Detail)
		}
	})
}

// Sound is true only when all three controls held; an inconclusive run — no matter the
// control fields — proves nothing and is not sound.
func TestSelfTestReportSound(t *testing.T) {
	pass := Invariant{Passed: true}
	fail := Invariant{Passed: false}
	cases := []struct {
		name          string
		pos, neg, swp Invariant
		inconclusive  bool
		want          bool
	}{
		{"all held", pass, pass, pass, false, true},
		{"positive failed", fail, pass, pass, false, false},
		{"negative failed (oracle can't catch a drop)", pass, fail, pass, false, false},
		{"swap failed (oracle can't catch a loss offset by a duplicate)", pass, pass, fail, false, false},
		{"inconclusive despite all marked pass", pass, pass, pass, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &SelfTestReport{PositiveControl: c.pos, NegativeControl: c.neg, SwapControl: c.swp, Inconclusive: c.inconclusive}
			if got := r.Sound(); got != c.want {
				t.Fatalf("Sound()=%v want %v", got, c.want)
			}
		})
	}
}
