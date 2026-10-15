// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A command its device's profile no longer defines is failed with the fixed reason, counted on
// its own series, and only for the devices the owner named: a device still defined, and a row
// already SENT, are untouched.
func TestAnUndefinedWaitingCommandIsFailedCountedAndConfinedToTheNamedDevices(t *testing.T) {
	api := realApi(t)
	seedHeldRow(t, api, "acme", "gone-q", "dev-1", model.CommandQueued, time.Now().Add(time.Hour))
	seedHeldRow(t, api, "acme", "gone-p", "dev-1", model.CommandParked, time.Now().Add(time.Hour))
	seedHeldRow(t, api, "acme", "kept", "dev-2", model.CommandQueued, time.Now().Add(time.Hour))
	seedHeldRow(t, api, "acme", "sent", "dev-1", model.CommandSent, time.Now().Add(-time.Hour))

	owner := fleetAssignments("plant-a")
	owner.byDevice = map[string]string{"dev-1": undefinedMark, "dev-2": "plant-a"}
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "undefined_total"})
	proc := &CommandDeliveryProcessor{Api: api, WindowOpenings: owner,
		DeliveryMetrics: DeliveryMetrics{WindowCommandsUndefined: counter}}

	require.NoError(t, proc.restampWindows(context.Background()))

	for _, token := range []string{"gone-q", "gone-p"} {
		got := rowOf(t, api, "acme", token)
		require.Equal(t, model.CommandFailed.String(), got.Status, token)
		require.Equal(t, model.OutcomeNotDelivered.String(), got.Outcome.String, token)
		require.Equal(t, sql.NullString{String: model.CommandUndefinedReason, Valid: true}, got.Error, token)
	}
	require.Equal(t, model.CommandQueued.String(), rowOf(t, api, "acme", "kept").Status,
		"a device whose profile still defines the command was failed")
	require.Equal(t, model.CommandSent.String(), rowOf(t, api, "acme", "sent").Status,
		"a command already sent was failed")
	require.Equal(t, 2.0, testutil.ToFloat64(counter), "failed rows are not counted")
}

// FailUndefinedWaiting names its rows by window, key and device, and nothing else.
func TestFailUndefinedWaitingIsConfinedToWindowKeyAndDevice(t *testing.T) {
	api := realApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedHeldRow(t, api, "acme", "hit", "dev-1", model.CommandHeld, time.Now().Add(time.Hour))
	other := &model.Command{DeviceToken: "dev-1", Name: "read", Status: model.CommandQueued.String(),
		WindowToken: sql.NullString{String: "plant-a", Valid: true}}
	other.Token = "other-key"
	require.NoError(t, api.RDB.DB(ctx).Create(other).Error)
	elsewhere := &model.Command{DeviceToken: "dev-1", Name: "reboot", Status: model.CommandQueued.String(),
		WindowToken: sql.NullString{String: "plant-b", Valid: true}}
	elsewhere.Token = "other-window"
	require.NoError(t, api.RDB.DB(ctx).Create(elsewhere).Error)

	n, err := api.FailUndefinedWaiting(ctx, "plant-a", "reboot", []string{"dev-1"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, model.CommandFailed.String(), rowOf(t, api, "acme", "hit").Status)
	require.Equal(t, model.CommandQueued.String(), rowOf(t, api, "acme", "other-key").Status)
	require.Equal(t, model.CommandQueued.String(), rowOf(t, api, "acme", "other-window").Status)
}
