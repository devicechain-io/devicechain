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
	"github.com/stretchr/testify/require"
)

func TestDeletedWindowKeepsUndecidedCommandsClosedAndConverges(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(fmt.Sprint(outage), func(t *testing.T) {
			api := realApi(t)
			seedFleetOnA(t, api)
			owner := fleetAssignments("plant-b")
			owner.answers["plant-a"] = model.WindowOpening{Found: false}
			delete(owner.byDevice, "dev-01")
			if outage {
				owner.err = errors.New("owner unavailable")
			}
			proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner}
			err := proc.restampWindows(context.Background())
			if outage {
				require.ErrorIs(t, err, core.ErrPassPartial)
			} else {
				require.NoError(t, err)
			}
			ctx := core.WithTenant(context.Background(), "acme")
			for i := 0; i < repointFleet; i++ {
				if !outage && i != 1 {
					require.Equal(t, "plant-b", rowOf(t, api, "acme", fmt.Sprintf("c-%02d", i)).WindowToken.String)
					continue
				}
				row := rowOf(t, api, "acme", fmt.Sprintf("c-%02d", i))
				require.True(t, row.WindowToken.Valid)
				require.Equal(t, "plant-a", row.WindowToken.String)
				require.True(t, row.DispatchUntil.Valid && !row.DispatchUntil.Time.After(time.Now()))
				_, claimed, err := api.MarkSent(ctx, row.ID)
				require.NoError(t, err)
				require.False(t, claimed)
			}
			owner.err = nil
			owner.byDevice["dev-01"] = ""
			proc.lastWindowRestamp = time.Time{}
			require.NoError(t, proc.restampWindows(context.Background()))
			row := rowOf(t, api, "acme", "c-01")
			require.False(t, row.WindowToken.Valid || row.DispatchAfter.Valid || row.DispatchUntil.Valid)
			_, claimed, err := api.MarkSent(ctx, row.ID)
			require.NoError(t, err)
			require.True(t, claimed)
		})
	}
}

func TestResumeCannotDispatchADeletedWindowsCommand(t *testing.T) {
	for _, target := range []string{"plant-b", ""} {
		t.Run(target, func(t *testing.T) {
			api := realApi(t)
			seedHeldRow(t, api, "acme", "resume", "dev-01", model.CommandHeld, time.Now().Add(-time.Hour))
			owner := fleetAssignments(target)
			owner.answers["plant-a"] = model.WindowOpening{Found: false}
			delete(owner.byDevice, "dev-01")
			writer := &recordingWriter{}
			proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner, DeviceCommandsWriter: writer}
			require.NoError(t, proc.restampWindows(context.Background()))
			require.NoError(t, proc.ResumeDevice(context.Background(), "acme", "dev-01"))
			row := rowOf(t, api, "acme", "resume")
			require.Equal(t, model.CommandQueued.String(), row.Status)
			require.Equal(t, "plant-a", row.WindowToken.String)
			require.Zero(t, writer.count())
			owner.byDevice["dev-01"] = target
			proc.lastWindowRestamp = time.Time{}
			require.NoError(t, proc.restampWindows(context.Background()))
			row = rowOf(t, api, "acme", "resume")
			if target == "" {
				require.False(t, row.WindowToken.Valid || row.DispatchUntil.Valid || row.DispatchAfter.Valid)
			} else {
				require.Equal(t, target, row.WindowToken.String)
				require.True(t, owner.answers[target].Closes.Equal(row.DispatchUntil.Time))
			}
			sweepOnce(t, proc)
			if target == "" {
				require.Equal(t, 1, writer.count())
				require.Equal(t, model.CommandSent.String(), rowOf(t, api, "acme", "resume").Status)
			} else {
				require.Zero(t, writer.count())
			}
		})
	}
}

func TestDeletedWindowStillMovesAllDecidedDevices(t *testing.T) {
	for _, target := range []string{"plant-b", ""} {
		t.Run(target, func(t *testing.T) {
			api := realApi(t)
			seedFleetOnA(t, api)
			owner := fleetAssignments(target)
			owner.answers["plant-a"] = model.WindowOpening{Found: false}
			proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner}
			require.NoError(t, proc.restampWindows(context.Background()))
			for i := 0; i < repointFleet; i++ {
				row := rowOf(t, api, "acme", fmt.Sprintf("c-%02d", i))
				if target == "" {
					require.False(t, row.WindowToken.Valid || row.DispatchAfter.Valid || row.DispatchUntil.Valid)
				} else {
					require.Equal(t, target, row.WindowToken.String)
				}
			}
		})
	}
}
