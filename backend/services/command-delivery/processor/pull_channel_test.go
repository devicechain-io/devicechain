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

// pullSet is a PullResolver that names a fixed set of devices, or fails.
type pullSet struct {
	pull map[string]bool
	err  error
}

func (p pullSet) PullDevices(context.Context, []string) (map[string]bool, error) {
	return p.pull, p.err
}

// TestAPullDevicesCommandIsHeldNotPublished is the channel's whole point on the platform side: a
// device that cannot be published to must not have its command published and marked SENT, which
// would end as a TIMEOUT blaming a device that was never given it.
func TestAPullDevicesCommandIsHeldNotPublished(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	// The projection says the device is present and live: only the channel can hold it.
	proc.Presence = presence.WithPullChannels(
		&scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}},
		pullSet{pull: map[string]bool{"dev-c1": true}})

	proc.sweepLocked(context.Background())

	if writer.count() != 0 {
		t.Fatalf("a pull device's command was published (%d); it reaches nothing and strands as SENT", writer.count())
	}
	if len(api.held) != 1 || api.held[0] != 1 {
		t.Fatalf("a pull device's command must be HELD for its pull, got held=%v", api.held)
	}
	if len(api.markedSent) != 0 {
		t.Fatalf("a held command must not be claimed by the sweep, marked sent = %v", api.markedSent)
	}
}

// TestADeviceNotOnThePullChannelStillDispatches is the counterweight: every assertion above is
// also satisfied by a layer that holds everything, which would stop command delivery.
func TestADeviceNotOnThePullChannelStillDispatches(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = presence.WithPullChannels(
		&scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}},
		pullSet{pull: map[string]bool{"someone-else": true}})

	proc.sweepLocked(context.Background())

	if writer.count() != 1 || len(api.held) != 0 {
		t.Fatalf("a device off the pull channel must dispatch as before: published %d, held %v", writer.count(), api.held)
	}
}

// TestWithoutAPullLayerBehaviourIsUnchanged is the golden for an instance that opted nothing in:
// no layer, the same presence reader, the same result as before the channel existed.
func TestWithoutAPullLayerBehaviourIsUnchanged(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}}

	proc.sweepLocked(context.Background())

	if writer.count() != 1 || len(api.held) != 0 || len(api.markedSent) != 1 {
		t.Fatalf("published %d held %v marked %v; want the pre-channel behaviour", writer.count(), api.held, api.markedSent)
	}
}

// TestAnUndecidableChannelHoldsRatherThanPublishes: when device-management cannot say which
// devices are pull devices, publishing risks stranding a pull device's command; holding only delays
// anyone else's.
func TestAnUndecidableChannelHoldsRatherThanPublishes(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = presence.WithPullChannels(
		&scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}},
		pullSet{err: errors.New("device-management unreachable")})

	proc.sweepLocked(context.Background())

	if writer.count() != 0 || len(api.held) != 1 {
		t.Fatalf("published %d held %v; an undecided channel must hold", writer.count(), api.held)
	}
}

// TestTheHoldReconcilerNeverReleasesAPullDevicesCommand: the reconciler asks the same Decide, so
// the command stays HELD for the pull however present the device looks.
func TestTheHoldReconcilerNeverReleasesAPullDevicesCommand(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, presence.WithPullChannels(
		&scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}},
		pullSet{pull: map[string]bool{"dev-c1": true}}))

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 0 {
		t.Fatalf("a pull device's held command was released: %v", api.releasedHolds)
	}
}

// TestTheHoldReconcilerStillReleasesOtherDevices is its counterweight.
func TestTheHoldReconcilerStillReleasesOtherDevices(t *testing.T) {
	api := &fakeApi{heldPage: []*model.Command{heldFor(1, "c1", "acme")}}
	proc := reconcilerWith(api, presence.WithPullChannels(
		&scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}},
		pullSet{pull: map[string]bool{}}))

	proc.reconcileHolds(context.Background())

	if len(api.releasedHolds) != 1 {
		t.Fatalf("a returned device off the pull channel must still be released, got %v", api.releasedHolds)
	}
}
