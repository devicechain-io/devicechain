// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-simulator/sim"
)

// fakeAgg serves a fixed set of stored aggregates and counts how often it is read.
type fakeAgg struct {
	stored map[string]DeviceAggregate
	reads  int
	// after, when set, replaces the answer from the second read on.
	after map[string]DeviceAggregate
	err   error
}

func (f *fakeAgg) DeviceAggregates(context.Context, Window) (map[string]DeviceAggregate, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	if f.reads > 1 && f.after != nil {
		return f.after, nil
	}
	return f.stored, nil
}

// fakeExact is the drill-down reader: the exact stored instants per device. readStoredIdentities
// calls it from a pool of workers, so the record of what was read is guarded.
type fakeExact struct {
	times map[string][]int64

	mu    sync.Mutex
	reads []string
}

func (f *fakeExact) DeviceIdentities(_ context.Context, dev string, _ Window) ([]int64, int, error) {
	f.mu.Lock()
	f.reads = append(f.reads, dev)
	f.mu.Unlock()
	return f.times[dev], 1, nil
}

// readDevices is a copy of the devices read so far, in the order the pool read them.
func (f *fakeExact) readDevices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reads...)
}

// storedOf is what a faithful store holds for the ledger's accepted events, as aggregates.
func storedOf(times map[string][]int64) map[string]DeviceAggregate {
	out := map[string]DeviceAggregate{}
	for d, ts := range times {
		if len(ts) > 0 {
			out[d] = AggregateOf(ts)
		}
	}
	return out
}

func copyTimes(in map[string][]int64) map[string][]int64 {
	out := make(map[string][]int64, len(in))
	for k, v := range in {
		out[k] = append([]int64(nil), v...)
	}
	return out
}

// fleet builds a ledger of nDev devices with perDev accepted events each, a millisecond
// apart, and a consistent driver snapshot.
func fleet(nDev, perDev int) (sim.IdentitySnapshot, sim.Snapshot, map[string][]int64) {
	devs := map[string]sim.DeviceIdentities{}
	stored := map[string][]int64{}
	for d := 0; d < nDev; d++ {
		name := "dev-" + string(rune('a'+d%26)) + string(rune('a'+d/26))
		var acc []int64
		for i := 0; i < perDev; i++ {
			acc = append(acc, idBase+int64(d)*7+int64(i+1)*1000)
		}
		devs[name] = sim.DeviceIdentities{Accepted: acc}
		stored[name] = append([]int64(nil), acc...)
	}
	led := ledgerOfIDs(devs)
	return led, sim.Snapshot{Emitted: led.All.Accepted}, stored
}

var testWindow = Window{Start: time.UnixMicro(idBase - 5e6).UTC(), End: time.UnixMicro(idBase + 5e6).UTC()}

func check(t *testing.T, exp *AggregateLedger, stored map[string]DeviceAggregate, opt AggregateOptions) AggregateReport {
	t.Helper()
	rep, inv, err := CheckAggregate(context.Background(), &fakeAgg{stored: stored}, exp, opt)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Name != InvAggregate || inv.Passed != rep.Reconciled {
		t.Fatalf("invariant %+v disagrees with the report (reconciled=%v)", inv, rep.Reconciled)
	}
	return rep
}

func TestAggregateIntactIsClean(t *testing.T) {
	led, drive, stored := fleet(40, 25)
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, false)
	rep := check(t, exp, storedOf(stored), AggregateOptions{})
	if !rep.Reconciled || rep.DevicesSound != 40 || rep.StoredRows != 1000 || rep.Accepted != 1000 {
		t.Fatalf("an intact store must reconcile: %+v", rep)
	}
}

func TestAggregateFindsOneDeletedRow(t *testing.T) {
	led, drive, stored := fleet(40, 25)
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, true)
	victim := stored["dev-ab"][7]
	stored["dev-ab"] = append(stored["dev-ab"][:7:7], stored["dev-ab"][8:]...)

	// Without the exact reader: found, named by device, a lower bound, and not reconciled.
	rep := check(t, exp, storedOf(stored), AggregateOptions{})
	if rep.Reconciled || rep.Missing != 1 || rep.DevicesMismatched != 1 || rep.Mismatches[0].Device != "dev-ab" || rep.Mismatches[0].Kind != "missing" {
		t.Fatalf("one deleted row: %+v", rep)
	}
	// With it: the exact identity.
	ex := &fakeExact{times: stored}
	rep = check(t, exp, storedOf(stored), AggregateOptions{Exact: ex})
	if rep.Reconciled || rep.Missing != 1 || !reflect.DeepEqual(rep.Samples.Missing, []string{key("dev-ab", victim)}) || rep.DevicesDrilled != 1 {
		t.Fatalf("the drill-down must name the deleted identity %s: %+v", key("dev-ab", victim), rep)
	}
	if !reflect.DeepEqual(ex.readDevices(), []string{"dev-ab"}) {
		t.Fatalf("only the mismatching device may be read exactly, read %v", ex.readDevices())
	}
}

func TestAggregateFindsOneDuplicatedRow(t *testing.T) {
	led, drive, stored := fleet(40, 25)
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, true)
	dup := stored["dev-cb"][3]
	stored["dev-cb"] = append(stored["dev-cb"], dup)
	rep := check(t, exp, storedOf(stored), AggregateOptions{Exact: &fakeExact{times: stored}})
	if rep.Reconciled || rep.DuplicateKeys != 1 || rep.ExtraCopies != 1 || !strings.HasPrefix(rep.Samples.Duplicate[0], key("dev-cb", dup)) {
		t.Fatalf("one duplicated row: %+v", rep)
	}
}

// 🔴 The case equal totals cannot see, in both shapes: the lost row and the duplicate on two
// DIFFERENT devices (the count per device moves, the grand total does not), and on ONE device
// (A,A,C,D stored for A,B,C,D: even the device's count is unchanged, only the hash moves).
func TestAggregateCatchesLossOffsetByDuplicate(t *testing.T) {
	t.Run("different devices", func(t *testing.T) {
		led, drive, stored := fleet(10, 10)
		exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, true)
		stored["dev-aa"] = stored["dev-aa"][1:]
		stored["dev-ba"] = append(stored["dev-ba"], stored["dev-ba"][0])
		agg := storedOf(stored)
		var rows int64
		for _, a := range agg {
			rows += a.Count
		}
		if rows != 100 {
			t.Fatalf("fixture: totals must still agree, got %d", rows)
		}
		rep := check(t, exp, agg, AggregateOptions{Exact: &fakeExact{times: stored}})
		if rep.Reconciled || rep.Missing != 1 || rep.DuplicateKeys != 1 {
			t.Fatalf("a loss offset by a duplicate passed: %+v", rep)
		}
	})
	t.Run("one device", func(t *testing.T) {
		led, drive, stored := fleet(10, 10)
		exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, true)
		lost := stored["dev-aa"][4]
		stored["dev-aa"][4] = stored["dev-aa"][3] // B replaced by a second A
		agg := storedOf(stored)
		if agg["dev-aa"].Count != 10 {
			t.Fatal("fixture: the device count must be unchanged")
		}
		// Counts alone: blind. That is the point of the hash.
		if agg["dev-aa"].Count != exp.Devices["dev-aa"].Accepted.Count {
			t.Fatal("fixture")
		}
		rep := check(t, exp, agg, AggregateOptions{})
		if rep.Reconciled || rep.Substituted != 1 || rep.Mismatches[0].Kind != "substituted" {
			t.Fatalf("a substitution must be seen by the hash: %+v", rep)
		}
		rep = check(t, exp, agg, AggregateOptions{Exact: &fakeExact{times: stored}})
		if rep.Reconciled || rep.Missing != 1 || rep.DuplicateKeys != 1 || rep.Samples.Missing[0] != key("dev-aa", lost) {
			t.Fatalf("exact: %+v", rep)
		}
	})
}

func TestAggregateUnledgeredDeviceAndRefusedStoredAreUnexpected(t *testing.T) {
	led, drive, stored := fleet(5, 10)
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, true)
	agg := storedOf(stored)
	agg["somebody-else"] = AggregateOf([]int64{idBase + 1})
	rep := check(t, exp, agg, AggregateOptions{})
	if rep.Reconciled || rep.Unexpected != 1 || rep.Mismatches[0].Kind != "unledgered" {
		t.Fatalf("rows for a device nobody sent for: %+v", rep)
	}

	// A refused send that was stored anyway.
	devs := map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idB}, Refused: []int64{idC}}}
	l2 := ledgerOfIDs(devs)
	d2 := sim.Snapshot{Emitted: 2, Shed: 1}
	exp2 := NewAggregateLedger("t1", "m", 1, testWindow, l2, d2, false)
	if exp2.Devices["d1"].Detail == nil {
		t.Fatal("a device with a refused send must keep its identities in the file")
	}
	st := map[string][]int64{"d1": {idA, idB, idC}}
	rep = check(t, exp2, storedOf(st), AggregateOptions{Exact: &fakeExact{times: st}})
	if rep.Reconciled || rep.Unexpected != 1 {
		t.Fatalf("a stored refusal: %+v", rep)
	}
	st = map[string][]int64{"d1": {idA, idB}}
	if rep = check(t, exp2, storedOf(st), AggregateOptions{}); !rep.Reconciled {
		t.Fatalf("a refusal that was NOT stored is clean: %+v", rep)
	}
}

// Ambiguous sends may or may not be stored: none, all, or any subset is sound, a duplicate of
// one is not.
func TestAggregateAmbiguousSubsets(t *testing.T) {
	acc := []int64{idA, idB}
	amb := []int64{idC, idD}
	led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: acc, Ambiguous: amb}})
	drive := sim.Snapshot{Emitted: 2, Failed: 2}
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, drive, false)

	for name, st := range map[string][]int64{
		"none stored": {idA, idB}, "all stored": {idA, idB, idC, idD},
		"one stored": {idA, idB, idD}, "the other": {idA, idB, idC},
	} {
		m := map[string][]int64{"d1": st}
		withExact := check(t, exp, storedOf(m), AggregateOptions{Exact: &fakeExact{times: m}})
		if !withExact.Reconciled {
			t.Errorf("%s must be sound with the drill-down: %+v", name, withExact)
		}
	}
	// The two ends (none stored, all stored) are decided by the aggregate ALONE, no exact read.
	for name, st := range map[string][]int64{"none stored": {idA, idB}, "all stored": {idA, idB, idC, idD}} {
		m := map[string][]int64{"d1": st}
		if rep := check(t, exp, storedOf(m), AggregateOptions{}); !rep.Reconciled || rep.DevicesDrilled != 0 {
			t.Errorf("%s must be sound from the aggregate alone: %+v", name, rep)
		}
	}
	// The partial states need the exact read; the aggregate alone refuses to guess.
	m := map[string][]int64{"d1": {idA, idB, idC}}
	if rep := check(t, exp, storedOf(m), AggregateOptions{}); rep.Reconciled || rep.Unresolved != 1 {
		t.Errorf("a partial ambiguous state without the exact read must not be called clean: %+v", rep)
	}
	for name, st := range map[string][]int64{
		"an ambiguous duplicated": {idA, idB, idC, idC}, "an accepted lost": {idA, idC, idD}, "an accepted duplicated": {idA, idA, idB},
	} {
		m := map[string][]int64{"d1": st}
		if rep := check(t, exp, storedOf(m), AggregateOptions{Exact: &fakeExact{times: m}}); rep.Reconciled {
			t.Errorf("%s passed: %+v", name, rep)
		}
	}
}

func TestAggregateInconclusiveCases(t *testing.T) {
	// Nothing sent.
	empty := NewAggregateLedger("t1", "m", 1, testWindow, ledgerOfIDs(map[string]sim.DeviceIdentities{}), sim.Snapshot{}, false)
	if rep := check(t, empty, nil, AggregateOptions{}); rep.Reconciled {
		t.Fatalf("an empty ledger is not a pass: %+v", rep)
	}
	// Ledger and driver disagree.
	led, _, stored := fleet(3, 3)
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, sim.Snapshot{Emitted: 99}, false)
	if rep := check(t, exp, storedOf(stored), AggregateOptions{}); rep.Reconciled || len(rep.Inconclusive) == 0 {
		t.Fatalf("a ledger the driver disowns must be inconclusive: %+v", rep)
	}
	// A store that moves between two reads.
	led, drive, stored := fleet(3, 3)
	exp = NewAggregateLedger("t1", "m", 1, testWindow, led, drive, false)
	// Rows still arriving: the first read is short a row, the second is whole. The SECOND read is
	// clean, so only the movement itself can make this inconclusive.
	short := storedOf(stored)
	short["dev-aa"] = AggregateOf(stored["dev-aa"][1:])
	f := &fakeAgg{stored: short, after: storedOf(stored)}
	rep, _, err := CheckAggregate(context.Background(), f, exp, AggregateOptions{Settle: time.Millisecond})
	if err != nil || rep.Reconciled || f.reads != 2 || len(rep.Inconclusive) == 0 || !strings.Contains(rep.Inconclusive[0], "changed") {
		t.Fatalf("a store that moved between two reads must be inconclusive even when the last read is clean: reads=%d err=%v %+v", f.reads, err, rep)
	}
	// And a store that holds still across both reads is decided.
	f = &fakeAgg{stored: storedOf(stored)}
	if rep, _, err = CheckAggregate(context.Background(), f, exp, AggregateOptions{Settle: time.Millisecond}); err != nil || !rep.Reconciled || f.reads != 2 {
		t.Fatalf("a still store must reconcile after two reads: reads=%d err=%v %+v", f.reads, err, rep)
	}
	// A read failure is an error, never a verdict.
	if _, _, err := CheckAggregate(context.Background(), &fakeAgg{err: errors.New("boom")}, exp, AggregateOptions{}); err == nil {
		t.Fatal("a failed read must be an error")
	}
}

func TestAggregateLedgerFileRoundTrip(t *testing.T) {
	led, drive, stored := fleet(6, 6)
	exp := NewAggregateLedger("t1", "m", 7, testWindow, led, drive, false)
	path := filepath.Join(t.TempDir(), "led.json")
	if err := exp.WriteFile(path); err != nil {
		t.Fatal(err)
	}
	back, err := ReadAggregateLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(exp.Devices, back.Devices) || back.Tenant != "t1" || !back.WindowStart.Equal(exp.WindowStart) {
		t.Fatalf("round trip changed the ledger")
	}
	// A hash above 2^63 must survive (a uint64 does not fit a JSON number).
	for _, a := range back.Devices {
		if a.Accepted.Hash == 0 {
			t.Fatal("hash lost")
		}
	}
	if rep := check(t, back, storedOf(stored), AggregateOptions{}); !rep.Reconciled {
		t.Fatalf("a ledger read back from disk must reconcile an intact store: %+v", rep)
	}
	if err := writeFileString(path, `{"schema":9,"tenant":"t","devices":{}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAggregateLedger(path); err == nil {
		t.Fatal("an unknown schema must be refused")
	}
}

// writeFileString is a test helper.
func writeFileString(path, s string) error { return os.WriteFile(path, []byte(s), 0o644) }

// MicrosHash is pinned to literal values: the SQL in pgperturb/aggregate.go must produce the
// same numbers, and the aggregate rig asserts it against a real server (negative instants
// included, since the encoding is two's complement).
func TestMicrosHashVectors(t *testing.T) {
	for us, want := range map[int64]uint64{
		0:                     0xaf5570f5a1810b7a,
		1:                     0xcd2662154e6d76b2,
		-1:                    0x12a3ae445661ce5d,
		1_700_000_000_000_000: 0xe5f449faadfdd2e6,
	} {
		if got := MicrosHash(us); got != want {
			t.Errorf("MicrosHash(%d) = %#016x, want %#016x", us, got, want)
		}
	}
}

// Two sends that share a (device, instant) cannot be told apart by a sum of hashes: one stored
// copy of each and two stored copies of one hash alike. The ledger records the collision and the
// oracle refuses to decide, as the per-identity read-back does.
func TestAggregateLedgerCollisionIsInconclusive(t *testing.T) {
	led := ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA, idA, idB}}})
	exp := NewAggregateLedger("t1", "m", 1, testWindow, led, sim.Snapshot{Emitted: 3}, true)
	if exp.Collisions != 1 {
		t.Fatalf("collisions = %d, want 1", exp.Collisions)
	}
	// Stored: one A lost, B duplicated: counts and hash sums are not equal here, but the stored
	// pair for {A,A,B} is what a faithful store holds, and that must still not read as clean.
	st := map[string][]int64{"d1": {idA, idA, idB}}
	rep := check(t, exp, storedOf(st), AggregateOptions{})
	if rep.Reconciled || len(rep.Inconclusive) == 0 || !strings.Contains(strings.Join(rep.Inconclusive, ";"), "more than once") {
		t.Fatalf("a ledger with a colliding identity must be inconclusive even when the store matches it: %+v", rep)
	}
	// Across outcomes too: the same instant accepted once and ambiguous once.
	led = ledgerOfIDs(map[string]sim.DeviceIdentities{"d1": {Accepted: []int64{idA}, Ambiguous: []int64{idA}}})
	if got := NewAggregateLedger("t1", "m", 1, testWindow, led, sim.Snapshot{Emitted: 1, Failed: 1}, false).Collisions; got != 1 {
		t.Fatalf("a collision across outcomes was not counted: %d", got)
	}
	// And a clean fleet records none.
	l2, d2, _ := fleet(5, 5)
	if got := NewAggregateLedger("t1", "m", 1, testWindow, l2, d2, false).Collisions; got != 0 {
		t.Fatalf("a clean ledger reported %d collisions", got)
	}
}
