// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
)

// partialReader answers for some devices and not others, with an error — the shape the
// real chunked reader produces when one chunk of a large tenant's read fails.
type partialReader struct {
	states map[string]presence.State
	err    error
}

func (p *partialReader) StatesFor(context.Context, []string) (map[string]presence.State, error) {
	return p.states, p.err
}

// TestAPartialReadStillReleasesTheDevicesItDidRead is the other half, and without it the
// test above is satisfied by a reconciler that never releases anything.
//
// 🔑 THE TWO DIRECTIONS COME FROM ONE READ. The devices the read covered are decided
// normally — including released when they are back — while only the ones it could not
// cover are skipped. A blanket "any error means do nothing" would be safe and would also
// throw away the answers already in hand, which is the cost this change removes.
func TestAPartialReadStillReleasesTheDevicesItDidRead(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{
		heldFor(1, "c1", "acme"), // read: back online ⇒ release
		heldFor(2, "c2", "acme"), // unread ⇒ keep holding
	}}
	proc := reconcilerWith(api, &partialReader{
		states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)},
		err:    errors.New("one chunk of the read failed"),
	})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 || api.releasedHolds[0] != 1 {
		t.Fatalf("the device the read DID cover must still be released, got %v", api.releasedHolds)
	}
}

// TestADeviceTheProjectionHasNeverSeenIsReleased pins the case that must NOT be confused
// with an unread one.
//
// 🔑 THIS IS WHY Resolved TAKES THE ERROR. Both a never-seen device and an unreached one
// are absent from the map, and they need opposite treatment: a device with no row genuinely
// releases — deliberately, so the sweep re-decides it in one place, and because a hold
// placed on a device that no longer reports as absent has nothing keeping it. Making
// absence mean "keep holding" would strand those commands until their TTL. So this test
// is the guard against over-correcting the one above.
func TestADeviceTheProjectionHasNeverSeenIsReleased(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, &partialReader{
		states: map[string]presence.State{}, // read succeeded; the projection simply has no row
	})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 {
		t.Fatalf("a device the projection has no row for must be released so the sweep re-decides it, got %v",
			api.releasedHolds)
	}
}

// TestAFailedReadReleasesNOTHING is the fail-closed rule of the hold reconciler, and it
// guards the single most dangerous line in this change.
//
// 🔴🔴 THE RECONCILER RELEASES ON *NOT-Hold*, WHICH MAKES SILENCE MEAN "RELEASE". A device
// the read could not cover is absent from the map; the zero State is Known:false; Decide
// answers Dispatch; Dispatch is not Hold — so without the Resolved check, a device-state
// blip RELEASES every withheld command in the batch. Those commands are then published at
// brokers where the devices are authoritatively absent, and every one lands in SENT and
// then TIMEOUT: the record that blames the device for the platform's outage, which is the
// exact outcome the HELD state was introduced to stop writing.
//
// 🔑 THE READER HERE NAMES NOTHING, WHICH IS THE COMMON CASE RATHER THAN THE EXOTIC ONE —
// it is what a whole-service outage produces. An earlier version of this change had the
// reader name the devices its failed chunks covered and had the reconciler skip exactly
// those; this test is why that was not enough on its own. Reader is an interface, and the
// naming can simply not happen. The rule that survives is on the ERROR.
//
// The reconciler reads at most reconcilePageSize devices per pass and the reader chunks at
// the same number, so a failing read today means the WHOLE batch is unresolved.
func TestAFailedReadReleasesNOTHING(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{
		heldFor(1, "c1", "acme"),
		heldFor(2, "c2", "acme"),
	}}
	// Note what is NOT set: no states at all. Only the error.
	proc := reconcilerWith(api, &partialReader{err: errors.New("device-state is unavailable")})

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 0 {
		t.Fatalf("a reader that failed without naming which devices it failed on must not cause "+
			"releases; got %v", api.releasedHolds)
	}
}

// TestTheSweepDispatchesUnreadDevices states the OTHER caller's direction, which is the
// opposite one and is equally deliberate.
//
// 🔴 THE GATE MAY ONLY WITHHOLD ON POSITIVE EVIDENCE OF ABSENCE. "We could not ask" is not
// evidence, so it dispatches — treating an unreachable device-state as "everything is
// absent" would convert one service's outage into a platform-wide command stall, which is
// strictly worse than the silent loss the gate prevents.
func TestTheSweepDispatchesUnreadDevices(t *testing.T) {
	api := &fakeApi{pending: []*model.Command{queuedFor(1, "c1", "acme")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &partialReader{err: errors.New("device-state is unavailable")}

	proc.deliverPendingCommands(context.Background())

	if len(writer.messages) != 1 {
		t.Fatalf("a command whose device could not be read must still dispatch, published %d", len(writer.messages))
	}
	if len(api.held) != 0 {
		t.Fatalf("an unreadable presence must never withhold, held %v", api.held)
	}
}
