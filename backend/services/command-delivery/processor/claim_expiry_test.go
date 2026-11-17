// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestResumeDeviceDoesNotPublishExpiredUnsweptCommand(t *testing.T) {
	api := realApi(t)
	writer := &recordingWriter{}
	refused := prometheus.NewCounter(prometheus.CounterOpts{Name: "refused_total"})
	proc := &CommandDeliveryProcessor{Api: api, DeviceCommandsWriter: writer,
		DeliveryMetrics: DeliveryMetrics{ClaimsRefusedDisabled: refused}}
	ctx := gateTenantCtx()
	cmd := queueFor(t, api, "expired", "dev-1")
	_, err := api.SetDeviceDisabled(ctx, "dev-1", true)
	require.NoError(t, err)
	held, err := api.HoldCommand(ctx, cmd.ID)
	require.NoError(t, err)
	require.True(t, held)
	require.NoError(t, api.RDB.DB(ctx).Model(cmd).Update("expires_at",
		sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}).Error)
	_, err = api.SetDeviceDisabled(ctx, "dev-1", false)
	require.NoError(t, err)

	// Resume does not run ExpireStale: this is the gap between expiry and its sweep.
	require.NoError(t, proc.ResumeDevice(context.Background(), "acme", "dev-1"))
	require.Empty(t, writer.messages, "an enabled device must not receive its expired backlog")
	found, err := api.CommandsByToken(ctx, []string{cmd.Token})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, model.CommandQueued.String(), found[0].Status)
	require.False(t, found[0].DispatchNonce.Valid)
	require.False(t, found[0].SentTime.Valid)
	require.Zero(t, testutil.ToFloat64(refused), "expiry is not a disabled-device refusal")

	// Even when both predicates refuse, expiry must not be reported as disabled.
	_, err = api.SetDeviceDisabled(ctx, "dev-1", true)
	require.NoError(t, err)
	require.NoError(t, proc.deliverCommand(context.Background(), found[0], pathSweep, routing{}))
	require.Empty(t, writer.messages)
	require.Zero(t, testutil.ToFloat64(refused), "an expired row is left for the expiry sweep")
}
