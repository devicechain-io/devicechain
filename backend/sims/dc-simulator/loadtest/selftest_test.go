// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"testing"
	"time"
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

// The negative control passes ONLY when the production Await timed out BELOW the
// target (reached=false — the path a real drop reports through), the settled count
// is exactly accepted-1, AND the production completeness invariant fails as a drop.
// This is the anti–check-that-cannot-fail proof: the self-test must not pass by
// Await spuriously reaching, by the oracle going inconclusive for the wrong reason,
// nor by a mis-sized deletion. The accepted/intact columns are varied
// independently so the two roles cannot be conflated.
func TestNegativeControlDetected(t *testing.T) {
	const floor int64 = 50
	cases := []struct {
		name                    string
		accepted, intact, after int64
		failed                  int64
		reached                 bool
		want                    bool
		why                     string
	}{
		{"deleted one, timed out below target, completeness fails", 1000, 1000, 999, 0, false, true,
			"the designed detection"},
		{"Await spuriously reached the target", 1000, 1000, 999, 0, true, false,
			"reached=true is a broken quiesce, not a detected drop — the exact production false-PASS to catch"},
		{"nothing deleted — count unchanged", 1000, 1000, 1000, 0, false, false,
			"no drop; and 1000 != intact-1"},
		{"two deleted — ambiguous stimulus", 1000, 1000, 998, 0, false, false,
			"must be exactly accepted-1"},
		{"one short but drive was dirty — inconclusive", 1000, 1000, 999, 3, false, false,
			"failed>0 makes completeness inconclusive, not a detected drop"},
		{"one short but below load floor — inconclusive", 40, 40, 39, 0, false, false,
			"accepted<floor: completeness reports inconclusive, not a drop"},
		{"accepted != intact — contaminated baseline", 1000, 1001, 1000, 0, false, false,
			"after==intact-1 (1000==1000) but Reconcile on accepted sees persisted==accepted → completeness PASSES"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := negativeControlDetected(c.accepted, c.intact, c.after, c.failed, floor, c.reached)
			if got != c.want {
				t.Fatalf("negativeControlDetected(acc=%d int=%d after=%d failed=%d reached=%v)=%v want %v (%s)",
					c.accepted, c.intact, c.after, c.failed, c.reached, got, c.want, c.why)
			}
		})
	}
}

// fakePerturber records whether it was invoked and returns a scripted deletion
// count — enough to pin evaluateControls' sequencing without a database.
type fakePerturber struct {
	deletes int
	err     error
	called  bool
}

func (f *fakePerturber) DeleteOneMeasurement(_ context.Context, _ Window) (int, error) {
	f.called = true
	return f.deletes, f.err
}

// evaluateControls' load-bearing sequencing has no live coverage otherwise: a
// failed positive control must short-circuit WITHOUT ever invoking the perturber
// (or the self-test would delete a row against an unproven baseline), and a
// mis-sized deletion must go inconclusive without a negative verdict. Driven with
// a scripted counter + fake perturber so both the happy path and the two guards
// are pinned.
func TestEvaluateControls(t *testing.T) {
	ctx := context.Background()
	const floor int64 = 50

	t.Run("sound: reaches intact, deletes one, times out below target", func(t *testing.T) {
		counter := &scriptedCounter{values: []int64{1000, 999}} // 1000 pre-delete, 999 after
		p := &fakePerturber{deletes: 1}
		out, err := evaluateControls(ctx, counter, p, Window{}, 1000, 0, floor, time.Millisecond, 40*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if !p.called {
			t.Fatal("perturber must be invoked once the positive control holds")
		}
		if out.inconclusive {
			t.Fatalf("unexpected inconclusive: %s", out.reason)
		}
		if !out.positive.Passed || !out.negative.Passed {
			t.Fatalf("both controls should pass: positive=%v negative=%v", out.positive.Passed, out.negative.Passed)
		}
		if out.persistedIntact != 1000 || out.persistedPerturbed != 999 || out.deleted != 1 {
			t.Fatalf("intact=%d perturbed=%d deleted=%d", out.persistedIntact, out.persistedPerturbed, out.deleted)
		}
	})

	t.Run("positive control fails: perturber never invoked", func(t *testing.T) {
		counter := &scriptedCounter{values: []int64{999}} // never reaches 1000
		p := &fakePerturber{deletes: 1}
		out, err := evaluateControls(ctx, counter, p, Window{}, 1000, 0, floor, time.Millisecond, 30*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if p.called {
			t.Fatal("perturber must NOT run when the baseline is unproven — it would delete against a dirty baseline")
		}
		if !out.inconclusive || out.positive.Passed {
			t.Fatalf("want inconclusive with failed positive; got inconclusive=%v positive=%v", out.inconclusive, out.positive.Passed)
		}
	})

	t.Run("mis-sized deletion: inconclusive, no negative verdict", func(t *testing.T) {
		counter := &scriptedCounter{values: []int64{1000}}
		p := &fakePerturber{deletes: 0} // perturber found nothing to delete
		out, err := evaluateControls(ctx, counter, p, Window{}, 1000, 0, floor, time.Millisecond, 30*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if !p.called || out.deleted != 0 {
			t.Fatalf("perturber called=%v deleted=%d", p.called, out.deleted)
		}
		if !out.inconclusive || out.negative.Passed {
			t.Fatalf("want inconclusive with no passed negative; got inconclusive=%v negative=%v", out.inconclusive, out.negative.Passed)
		}
	})
}

// Sound is true only when both controls held; an inconclusive run — no matter the
// control fields — proves nothing and is not sound.
func TestSelfTestReportSound(t *testing.T) {
	pass := Invariant{Passed: true}
	fail := Invariant{Passed: false}
	cases := []struct {
		name         string
		pos, neg     Invariant
		inconclusive bool
		want         bool
	}{
		{"both held", pass, pass, false, true},
		{"positive failed", fail, pass, false, false},
		{"negative failed (oracle can't catch a drop)", pass, fail, false, false},
		{"inconclusive despite both marked pass", pass, pass, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &SelfTestReport{PositiveControl: c.pos, NegativeControl: c.neg, Inconclusive: c.inconclusive}
			if got := r.Sound(); got != c.want {
				t.Fatalf("Sound()=%v want %v", got, c.want)
			}
		})
	}
}
