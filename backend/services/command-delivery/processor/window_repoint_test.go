// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAssignments answers both questions the re-stamp pass asks the owner: a window's opening and
// which window each device's type is on now. byDevice maps a device to the window its type uses
// now ("" = no window: ungated); a device absent from it is one the owner decides nothing about
// (disabled, gone).
type fakeAssignments struct {
	fakeOpenings
	byDevice map[string]string
	err      error
}

func (f *fakeAssignments) CurrentWindows(_ context.Context, devices []string, _ string) ([]model.DispatchHold, []string, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	var holds []model.DispatchHold
	var ungated []string
	for _, d := range devices {
		w, decided := f.byDevice[d]
		switch {
		case !decided:
		case w == "":
			ungated = append(ungated, d)
		default:
			o := f.answers[w]
			holds = append(holds, model.DispatchHold{DeviceToken: d, WindowToken: w,
				DispatchAfter: o.DispatchAfter, DispatchUntil: o.Closes})
		}
	}
	return holds, ungated, nil
}

const repointFleet = 50

// seedFleetOnA queues one reboot per device on fleet devices dev-00..dev-49, all stamped from
// window plant-a (the type's window at enqueue).
func seedFleetOnA(t *testing.T, api *model.Api) {
	t.Helper()
	for i := 0; i < repointFleet; i++ {
		seedHeldRow(t, api, "acme", fmt.Sprintf("c-%02d", i), fmt.Sprintf("dev-%02d", i), model.CommandQueued,
			time.Now().Add(time.Hour))
	}
}

func fleetAssignments(window string) *fakeAssignments {
	a := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)
	b := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	f := &fakeAssignments{fakeOpenings: fakeOpenings{answers: map[string]model.WindowOpening{
		"plant-a": {Found: true, DispatchAfter: a, Closes: a.Add(4 * time.Hour)},
		"plant-b": {Found: true, DispatchAfter: b, Closes: b.Add(4 * time.Hour)},
	}}, byDevice: map[string]string{}}
	for i := 0; i < repointFleet; i++ {
		f.byDevice[fmt.Sprintf("dev-%02d", i)] = window
	}
	return f
}

// The decision: a type moved from window A to window B takes its waiting commands with it. Before,
// all 50 stayed on A — still alive, still on A's schedule — and ran in the slot the operator had
// just moved the type off.
func TestARepointedTypesWaitingCommandsFollowItToTheNewWindow(t *testing.T) {
	api := realApi(t)
	seedFleetOnA(t, api)
	owner := fleetAssignments("plant-b")
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner}

	require.NoError(t, proc.restampWindows(context.Background()))

	b := owner.answers["plant-b"]
	for i := 0; i < repointFleet; i++ {
		got := rowOf(t, api, "acme", fmt.Sprintf("c-%02d", i))
		require.Equal(t, "plant-b", got.WindowToken.String, "command %d is still bound to the window its type left", i)
		require.True(t, got.DispatchAfter.Time.Equal(b.DispatchAfter) && got.DispatchUntil.Time.Equal(b.Closes),
			"command %d holds [%v, %v), want B's opening [%v, %v)", i,
			got.DispatchAfter.Time, got.DispatchUntil.Time, b.DispatchAfter, b.Closes)
		require.Equal(t, model.CommandQueued.String(), got.Status, "a re-point gates the start; it never dispatches")
	}
}

// Unassigning the type's window releases what was waiting on it: the type gates nothing now.
func TestAnUnassignedTypesWaitingCommandsAreReleased(t *testing.T) {
	api := realApi(t)
	seedFleetOnA(t, api)
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: fleetAssignments("")}

	require.NoError(t, proc.restampWindows(context.Background()))

	for i := 0; i < repointFleet; i++ {
		got := rowOf(t, api, "acme", fmt.Sprintf("c-%02d", i))
		require.False(t, got.WindowToken.Valid || got.DispatchAfter.Valid || got.DispatchUntil.Valid,
			"command %d is still held (%v, %v, %v) for a type with no window", i,
			got.WindowToken, got.DispatchAfter, got.DispatchUntil)
	}
}

// A device the owner decided nothing about (disabled, or gone) keeps its stamp, and a device whose
// type is still on A is restamped onto A's current opening as before — the re-point is per device.
func TestARepointMovesOnlyTheDevicesTheOwnerDecided(t *testing.T) {
	api := realApi(t)
	seedFleetOnA(t, api)
	owner := fleetAssignments("plant-b")
	owner.byDevice["dev-00"] = "plant-a"
	delete(owner.byDevice, "dev-01")
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner}

	require.NoError(t, proc.restampWindows(context.Background()))

	a := owner.answers["plant-a"]
	stay := rowOf(t, api, "acme", "c-00")
	assert.Equal(t, "plant-a", stay.WindowToken.String)
	assert.True(t, stay.DispatchAfter.Time.Equal(a.DispatchAfter), "a device still on A follows A's schedule")
	silent := rowOf(t, api, "acme", "c-01")
	assert.Equal(t, "plant-a", silent.WindowToken.String, "a device the owner was silent about was moved")
	assert.True(t, silent.DispatchAfter.Valid, "a device the owner was silent about was released")
	assert.Equal(t, "plant-b", rowOf(t, api, "acme", "c-02").WindowToken.String)
}

// An owner that cannot answer moves nothing; the window's own re-stamp still runs.
func TestARepointErrorLeavesTheRowsOnTheirWindow(t *testing.T) {
	api := realApi(t)
	seedFleetOnA(t, api)
	owner := fleetAssignments("")
	owner.err = errors.New("device-management is down")
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner}

	err := proc.restampWindows(context.Background())
	require.ErrorIs(t, err, core.ErrPassPartial)
	got := rowOf(t, api, "acme", "c-00")
	assert.Equal(t, "plant-a", got.WindowToken.String, "an outage released a waiting command")
	assert.True(t, got.DispatchAfter.Time.Equal(owner.answers["plant-a"].DispatchAfter),
		"the window's own re-stamp must still run")
}
