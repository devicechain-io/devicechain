// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// heldFor builds a withheld command for a tenant.
func heldFor(id uint, token, tenant string) *model.Command {
	cmd := queuedFor(id, token, tenant)
	cmd.Status = model.CommandHeld.String()
	return cmd
}

// reconcilerWith builds a processor whose reconcile lock is available.
func reconcilerWith(api *fakeApi, reader presence.Reader) *CommandDeliveryProcessor {
	api.reconcileLockAvailable = true
	proc := procWith(api, &recordingWriter{})
	proc.Presence = reader
	return proc
}

// TestAReturnedDeviceHasItsCommandsReleased is the reconciler's purpose.
//
// 🔴 WITHOUT IT THE GATE IS A ONE-WAY DOOR. A command withheld from a device that comes
// back thirty seconds later would otherwise sit in HELD until its TTL dragged it to
// EXPIRED — days — which is strictly worse than the silent loss the gate replaces,
// because the loss at least happened promptly. This is why the net ships WITH the gate.
func TestAReturnedDeviceHasItsCommandsReleased(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, &scriptedReader{
		states: map[string]presence.State{"dev-c1": asserted(true, "mqtt")},
	})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 || api.releasedHolds[0] != 1 {
		t.Fatalf("a returned device's withheld command must be released, got %v", api.releasedHolds)
	}
}

// TestAReleaseIsCOUNTED, and TestALostReleaseIsNot.
//
// 🔑 THE PAIR IS THE MEASUREMENT, NOT EITHER NUMBER. HoldsPlaced rising with
// HoldsReleased flat is the signature of a gate that has become a one-way door — commands
// going into HELD and nothing bringing them out — which is the failure mode this whole
// reconciler exists to prevent and which has no other symptom until a week of TTLs
// elapses. A releases meter that counted ATTEMPTS would drift upward against holds under
// exactly the contention that makes the pair worth reading, so it counts only releases
// that landed.
func TestAReleaseIsCOUNTED(t *testing.T) {
	for _, c := range []struct {
		name string
		lost bool
		want float64
	}{
		{"a release that landed", false, 1},
		{"a release that lost its race", true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
			api.releaseLoses = c.lost
			proc := reconcilerWith(api, &scriptedReader{
				states: map[string]presence.State{"dev-c1": asserted(true, "mqtt")},
			})
			proc.HoldsReleased = prometheus.NewCounter(prometheus.CounterOpts{
				Name: "holds_released_" + strconv.FormatBool(c.lost)})

			proc.reconcileHolds(context.Background())

			if got := testutil.ToFloat64(proc.HoldsReleased); got != c.want {
				t.Fatalf("HoldsReleased = %v, want %v", got, c.want)
			}
		})
	}
}

// TestAStillAbsentDeviceKeepsItsHold is the counterweight, and it is what stops the
// reconciler being a mechanism that simply undoes the gate on a timer. Every assertion
// above is also satisfied by a pass that releases everything it reads.
func TestAStillAbsentDeviceKeepsItsHold(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, &scriptedReader{
		states: map[string]presence.State{"dev-c1": asserted(false, "mqtt")},
	})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 0 {
		t.Fatalf("a device that is still absent must keep its hold, released %v", api.releasedHolds)
	}
}

// TestTheReconcilerFailsCLOSED.
//
// 🔴 THE OPPOSITE DIRECTION FROM THE SWEEP, AND THE SAME RULE: neither path may act on
// information it does not have. The gate's new behaviour on the sweep is WITHHOLDING, so
// an unreachable projection means don't withhold; the new behaviour here is RELEASING, so
// it means don't release.
//
// Releasing on a failed read would be the more damaging of the two mistakes by far. It
// would throw an absent fleet's entire accumulated backlog back into the dispatch path at
// exactly the moment the sweep is ALSO ungated — so every one of those commands would be
// published into the void and marked SENT, undoing days of the gate's work in one pass.
func TestTheReconcilerFailsCLOSED(t *testing.T) {
	cases := []struct {
		name   string
		reader presence.Reader
		reads  int
	}{
		{"no presence reader wired", nil, 0},
		{"the projection read failed", &scriptedReader{err: errors.New("device-state unreachable")}, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
			proc := reconcilerWith(api, c.reader)

			proc.reconcileHolds(context.Background())

			if len(api.releasedHolds) != 0 {
				t.Fatalf("nothing may be released without presence evidence, released %v",
					api.releasedHolds)
			}
			// With no reader at all the withheld set must not even be READ: there is
			// nothing that could be done with the answer, and the reconciler runs on
			// every pod of every instance that does not deploy device-state.
			if len(api.heldReads) != c.reads {
				t.Fatalf("expected %d reads of the withheld set, got %d", c.reads, len(api.heldReads))
			}
		})
	}
}

// TestTheReconcilerReleasesAnUndeliverableCommandRatherThanFailingItHere.
//
// 🔑 ONE PLACE DECIDES WHAT HAPPENS TO A DISPATCHABLE COMMAND. The reconciler asks only
// "is this device still absent?" — anything else releases, and the sweep re-asks the full
// question with a fresh read on the next tick. A row held before its device was known to
// be on a transport that carries no commands therefore goes back to QUEUED and is failed
// properly there, with a reason recorded, rather than being failed by a second decision
// site whose rules could drift from the first.
func TestTheReconcilerReleasesAnUndeliverableCommandRatherThanFailingItHere(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, &scriptedReader{
		states: map[string]presence.State{"dev-c1": asserted(true, "sparkplug")},
	})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 {
		t.Fatalf("an undeliverable hold must be released for the sweep to terminate, got %v",
			api.releasedHolds)
	}
	if len(api.undeliverable) != 0 {
		t.Fatalf("the reconciler must not reach a terminal state itself, got %v", api.undeliverable)
	}
}

// TestADeletedTenantsHoldsAreNeverReleased.
//
// 🔴 ADR-077 ON THIS PATH TOO. Releasing to QUEUED is not itself an actuation, which is
// exactly why it is easy to miss — but the sweep dispatches QUEUED rows, so a release is
// an actuation one tick later, on an offboarded customer's hardware. The gate lives on
// every path that can put a command back in front of the dispatcher, not only on the one
// that publishes.
func TestADeletedTenantsHoldsAreNeverReleased(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{
		heldFor(1, "c1", "acme"),
		heldFor(2, "c2", "other"),
	}}
	proc := reconcilerWith(api, &scriptedReader{states: map[string]presence.State{
		"dev-c1": asserted(true, "mqtt"),
		"dev-c2": asserted(true, "mqtt"),
	}})
	proc.TenantDeleted = func(tenant string) bool { return tenant == "acme" }

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 || api.releasedHolds[0] != 2 {
		t.Fatalf("only the live tenant's hold may be released, got %v", api.releasedHolds)
	}
}

// TestTheWalkAdvancesAndThenRestarts pins the cursor, which is what makes the bounded
// read fair.
//
// 🔑 A PLAIN `LIMIT n` WOULD WEDGE. With oldest-first ordering, the commands of a device
// that is never coming back hold the smallest ids and would fill every page forever — so
// the devices that DID return, holding higher ids, would never be looked at and the net
// would silently stop being a net while still reading a full page every pass. Walking
// from a cursor visits every row within a few passes regardless of what is stuck at the
// front, and resetting at the end is what makes it a cycle rather than a one-shot scan.
func TestTheWalkAdvancesAndThenRestarts(t *testing.T) {
	// A full page (so the fake reports a cursor) followed by a short one.
	page := make([]*model.Command, 0, reconcilePageSize+1)
	for i := 1; i <= reconcilePageSize+1; i++ {
		page = append(page, heldFor(uint(i), "c", "acme"))
	}
	api := &fakeApi{heldPage: page}
	proc := reconcilerWith(api, &scriptedReader{})

	proc.reconcileHolds(context.Background())
	if proc.reconcileCursor != uint(reconcilePageSize) {
		t.Fatalf("a full page must advance the cursor, got %d", proc.reconcileCursor)
	}

	proc.reconcileHolds(context.Background())
	if proc.reconcileCursor != 0 {
		t.Fatalf("a short page means the walk is done and must restart it; cursor=%d — parked past "+
			"the end, every later pass reads nothing and the reconciler quietly stops reconciling",
			proc.reconcileCursor)
	}
	if len(api.heldReads) != 2 || api.heldReads[0] != 0 || api.heldReads[1] != uint(reconcilePageSize) {
		t.Fatalf("the second pass must resume where the first stopped, reads=%v", api.heldReads)
	}
}

// TestTheReconcilerDoesNothingWithoutItsLock. It publishes nothing, so a concurrent pass
// would not double-actuate — but N replicas walking the same set every interval is N
// times the reads for one pass's worth of releases, and the lock is what makes the cost
// independent of replica count.
func TestTheReconcilerDoesNothingWithoutItsLock(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := procWith(api, &recordingWriter{})
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, "mqtt")}}
	api.reconcileLockAvailable = false

	proc.reconcileHolds(context.Background())

	if api.reconcileLockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", api.reconcileLockAttempts)
	}
	if len(api.heldReads) != 0 || len(api.releasedHolds) != 0 {
		t.Fatalf("a replica without the lock must not walk the withheld set: reads=%v released=%v",
			api.heldReads, api.releasedHolds)
	}
}

// TestTheReconcilerUsesItsOwnLock. Sharing the delivery sweep's lock would serialize a
// slow minutes-cadence walk against the 30-second delivery pass, so a long reconcile
// would starve delivery — the fast path losing to the slow one.
func TestTheReconcilerUsesItsOwnLock(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, &scriptedReader{})
	// The DELIVERY lock is held by a peer; the reconcile pass must run anyway.
	api.lockAvailable = false

	proc.reconcileHolds(context.Background())

	if len(api.heldReads) != 1 {
		t.Fatalf("the reconcile pass must not depend on the delivery sweep's lock, reads=%v",
			api.heldReads)
	}
	if api.lockAttempts != 0 {
		t.Fatalf("the reconcile pass must not touch the delivery lock at all, attempts=%d",
			api.lockAttempts)
	}
}
