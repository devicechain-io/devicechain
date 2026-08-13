// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// heldFor creates n HELD commands for one device and returns their ids.
func heldForDevice(t *testing.T, api *Api, ctx context.Context, device string, n int) []uint {
	t.Helper()
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		cmd := &Command{DeviceToken: device, Name: "reboot", Status: CommandHeld.String()}
		cmd.Token = fmt.Sprintf("%s-held-%04d", device, i)
		if err := api.RDB.DB(ctx).Create(cmd).Error; err != nil {
			t.Fatalf("seed %s/%d: %v", device, i, err)
		}
		ids = append(ids, cmd.ID)
	}
	return ids
}

// statusOf reads one command's current status.
func statusOf(t *testing.T, api *Api, ctx context.Context, id uint) string {
	t.Helper()
	return loadOrFail(t, api, ctx, id).Status
}

// TestTheWakeReleasesOnlyTheDeviceThatCameBack.
//
// 🔑 THE NEGATIVE HALF IS THE WHOLE TEST. Releasing the right device's commands is also
// what a wake that released EVERYTHING would do, and that failure is invisible: the sweep
// simply re-holds the other devices on its next tick, so the only symptom is a burst of
// pointless work and a churn of statuses nobody is watching.
func TestTheWakeReleasesOnlyTheDeviceThatCameBack(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	returned := heldForDevice(t, api, ctx, "pump-1", 3)
	stillAway := heldForDevice(t, api, ctx, "pump-2", 2)

	released, err := api.ReleaseHeldForDevice(ctx, "pump-1")
	if err != nil {
		t.Fatalf("ReleaseHeldForDevice failed: %v", err)
	}
	if released != 3 {
		t.Fatalf("released %d, want 3", released)
	}
	for _, id := range returned {
		if got := statusOf(t, api, ctx, id); got != CommandQueued.String() {
			t.Fatalf("the returned device's command %d is %s, want QUEUED", id, got)
		}
	}
	for _, id := range stillAway {
		if got := statusOf(t, api, ctx, id); got != CommandHeld.String() {
			t.Fatalf("a DIFFERENT device's command %d became %s; the wake released more than the "+
				"device that came back", id, got)
		}
	}
}

// 🔴 TestTheWakeCannotReachAnotherTENANTsDevice.
//
// Device tokens are unique per tenant, not globally, so two tenants can each own a
// "pump-1" — and the wake is called by a transport holding a device token and a tenant
// context, with no join back to the owning tenant except the scope on the query. If that
// scope were ever lost, one tenant's device connecting would release another tenant's
// commands into the dispatch path, and the sweep would then publish them to a device that
// never asked. This asserts the isolation directly rather than trusting it.
func TestTheWakeCannotReachAnotherTenantsDevice(t *testing.T) {
	api := newTestApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")
	mine := heldForDevice(t, api, acme, "pump-1", 2)
	theirs := heldForDevice(t, api, other, "pump-1", 2)

	released, err := api.ReleaseHeldForDevice(acme, "pump-1")
	if err != nil {
		t.Fatalf("ReleaseHeldForDevice failed: %v", err)
	}
	if released != 2 {
		t.Fatalf("released %d, want only this tenant's 2", released)
	}
	for _, id := range mine {
		if got := statusOf(t, api, acme, id); got != CommandQueued.String() {
			t.Fatalf("own command %d is %s, want QUEUED", id, got)
		}
	}
	for _, id := range theirs {
		if got := statusOf(t, api, other, id); got != CommandHeld.String() {
			t.Fatalf("ANOTHER TENANT's command %d became %s — the wake crossed a tenant boundary "+
				"on a device token both tenants legitimately use", id, got)
		}
	}
}

// TestTheWakeCannotReviveACommandThatIsNoLongerHeld walks every state a row could have
// moved to between the device connecting and this write landing. Each one is a command
// that is either in flight or finished, and returning any of them to the queue would put
// it in front of the dispatcher a second time.
func TestTheWakeCannotReviveACommandThatIsNoLongerHeld(t *testing.T) {
	states := append([]CommandStatus{CommandSent}, terminalStatuses...)
	for _, state := range states {
		t.Run(state.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "acme")
			ids := heldForDevice(t, api, ctx, "pump-1", 1)
			if err := api.RDB.DB(ctx).Model(&Command{}).Where("id = ?", ids[0]).
				Update("status", state.String()).Error; err != nil {
				t.Fatalf("staging %s failed: %v", state, err)
			}

			released, err := api.ReleaseHeldForDevice(ctx, "pump-1")
			if err != nil {
				t.Fatalf("ReleaseHeldForDevice failed: %v", err)
			}
			if released != 0 {
				t.Fatalf("the wake released a %s command", state)
			}
			if got := statusOf(t, api, ctx, ids[0]); got != state.String() {
				t.Fatalf("a %s command became %s", state, got)
			}
		})
	}
}

// TestWakingADeviceWithNothingWaitingIsFree is the ordinary case, and it must be quiet:
// most connects have no backlog at all, and the wake fires on every one of them.
func TestWakingADeviceWithNothingWaitingIsFree(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	released, err := api.ReleaseHeldForDevice(ctx, "never-seen")
	if err != nil {
		t.Fatalf("waking a device with no backlog must not error: %v", err)
	}
	if released != 0 {
		t.Fatalf("released %d for a device with nothing waiting", released)
	}
}

// TestTheWakeCountsWhatItActuallyReleased. The count is what the caller logs and meters,
// so it must be rows changed rather than rows examined — a wake reporting work it did not
// do would make a broken hold look like a busy one.
func TestTheWakeCountsWhatItActuallyReleased(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	heldForDevice(t, api, ctx, "pump-1", 4)
	// One of them is answered before the device comes back.
	if err := api.RDB.DB(ctx).Model(&Command{}).
		Where("token = ?", "pump-1-held-0002").
		Update("status", CommandSuccessful.String()).Error; err != nil {
		t.Fatalf("staging failed: %v", err)
	}

	released, err := api.ReleaseHeldForDevice(ctx, "pump-1")
	if err != nil {
		t.Fatalf("ReleaseHeldForDevice failed: %v", err)
	}
	if released != 3 {
		t.Fatalf("released count = %d, want 3 — the answered command must not be counted", released)
	}
}
