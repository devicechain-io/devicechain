// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type identitySource struct {
	fakeSource
	ids    map[string]uint64
	err    error
	calls  int
	tokens []string
}

func (s *identitySource) DeviceIncarnations(_ context.Context, tenant string, tokens []string) (map[string]uint64, error) {
	s.calls++
	s.tokens = append([]string{}, tokens...)
	return s.ids, s.err
}

func TestHeldReleaseVerifiesDeviceIncarnation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ids      map[string]uint64
		err      error
		want     model.CommandStatus
		releases int
	}{
		{name: "absent", ids: map[string]uint64{}, want: model.CommandCancelled},
		{name: "different", ids: map[string]uint64{"dev": 23}, want: model.CommandCancelled},
		{name: "matching", ids: map[string]uint64{"dev": 17}, want: model.CommandQueued, releases: 2},
		{name: "lookup error", ids: map[string]uint64{"dev": 17}, err: errors.New("owner down"), want: model.CommandHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := realApi(t)
			ctx := gateTenantCtx()
			var commands []*model.Command
			for _, token := range []string{"hold-1", "hold-2"} {
				cmd := queueFor(t, api, token, "dev")
				cmd.DeviceIncarnation = sql.NullInt64{Int64: 17, Valid: true}
				require.NoError(t, api.RDB.DB(ctx).Model(cmd).Update("device_incarnation", cmd.DeviceIncarnation).Error)
				held, err := api.HoldCommand(ctx, cmd.ID)
				require.NoError(t, err)
				require.True(t, held)
				commands = append(commands, cmd)
			}
			src := &identitySource{ids: tc.ids, err: tc.err}
			proc := &CommandDeliveryProcessor{Api: api, DisabledSource: src,
				Presence: &scriptedReader{states: map[string]presence.State{"dev": asserted(true, mqttSource)}}}
			proc.rememberSynced("acme")
			proc.HoldsCancelled = prometheus.NewCounter(prometheus.CounterOpts{Name: "holds_cancelled"})
			proc.DisabledGateErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "identity_errors"})
			assert.Equal(t, tc.releases, proc.reconcileTenantBatch(context.Background(), tenantBatch{tenant: "acme", commands: commands}))
			wantCancelled := 0
			if tc.want == model.CommandCancelled {
				wantCancelled = 2
			}
			assert.EqualValues(t, wantCancelled, testutil.ToFloat64(proc.HoldsCancelled))
			assert.Equal(t, 1, src.calls)
			assert.Equal(t, []string{"dev"}, src.tokens, "the batch looks up distinct tokens once")
			for _, token := range []string{"hold-1", "hold-2"} {
				assert.Equal(t, tc.want.String(), gateStatusOf(t, api, token))
				rows, err := api.CommandsByToken(ctx, []string{token})
				require.NoError(t, err)
				if tc.want == model.CommandCancelled {
					assert.Equal(t, model.OutcomeNotDelivered.String(), rows[0].Outcome.String)
				}
			}
			if tc.err != nil {
				assert.EqualValues(t, 1, testutil.ToFloat64(proc.DisabledGateErrors))
			}
		})
	}
}

func TestResumeDeviceVerifiesDeviceIncarnation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ids      map[string]uint64
		err      error
		want     model.CommandStatus
		releases int
	}{
		{name: "absent", ids: map[string]uint64{}, want: model.CommandCancelled},
		{name: "different", ids: map[string]uint64{"dev": 23}, want: model.CommandCancelled},
		{name: "matching", ids: map[string]uint64{"dev": 17}, want: model.CommandSent, releases: 2},
		{name: "lookup error", ids: map[string]uint64{"dev": 17}, err: errors.New("owner down"), want: model.CommandHeld},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := realApi(t)
			ctx := gateTenantCtx()
			var commands []*model.Command
			for _, token := range []string{"hold-1", "hold-2"} {
				cmd := queueFor(t, api, token, "dev")
				cmd.DeviceIncarnation = sql.NullInt64{Int64: 17, Valid: true}
				require.NoError(t, api.RDB.DB(ctx).Model(cmd).Update("device_incarnation", cmd.DeviceIncarnation).Error)
				held, err := api.HoldCommand(ctx, cmd.ID)
				require.NoError(t, err)
				require.True(t, held)
				commands = append(commands, cmd)
			}
			src := &identitySource{ids: tc.ids, err: tc.err}
			writer := &recordingWriter{}
			proc := &CommandDeliveryProcessor{Api: api, DisabledSource: src, DeviceCommandsWriter: writer,
				Presence: &scriptedReader{states: map[string]presence.State{"dev": asserted(true, mqttSource)}}}
			proc.rememberSynced("acme")
			proc.HoldsCancelled = prometheus.NewCounter(prometheus.CounterOpts{Name: "holds_cancelled"})
			proc.DisabledGateErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "identity_errors"})
			proc.HoldsReleased = prometheus.NewCounter(prometheus.CounterOpts{Name: "resume_releases"})
			require.NoError(t, proc.ResumeDevice(context.Background(), "acme", "dev"))
			assert.EqualValues(t, tc.releases, testutil.ToFloat64(proc.HoldsReleased))
			assert.Len(t, writer.devices, tc.releases)
			wantCancelled := 0
			if tc.want == model.CommandCancelled {
				wantCancelled = 2
			}
			assert.EqualValues(t, wantCancelled, testutil.ToFloat64(proc.HoldsCancelled))
			assert.Equal(t, 1, src.calls)
			assert.Equal(t, []string{"dev"}, src.tokens, "the batch looks up distinct tokens once")
			for _, token := range []string{"hold-1", "hold-2"} {
				assert.Equal(t, tc.want.String(), gateStatusOf(t, api, token))
				rows, err := api.CommandsByToken(ctx, []string{token})
				require.NoError(t, err)
				if tc.want == model.CommandCancelled {
					assert.Equal(t, model.OutcomeNotDelivered.String(), rows[0].Outcome.String)
				}
			}
			if tc.err != nil {
				assert.EqualValues(t, 1, testutil.ToFloat64(proc.DisabledGateErrors))
			}
		})
	}
}

func TestHoldPeriodicPassRetiresCoveredCommandsWithoutPresence(t *testing.T) {
	api := realApi(t)
	ctx := gateTenantCtx()
	at := time.Now().UTC().Add(-time.Hour)
	_, err := api.CancelDeletedDeviceCommands(ctx, "dev", 17, at)
	require.NoError(t, err)
	cmd := queueFor(t, api, "late-old", "dev")
	require.NoError(t, api.RDB.DB(ctx).Model(cmd).Update("device_incarnation", 17).Error)
	proc := &CommandDeliveryProcessor{Api: api, reconcileLock: func(_ context.Context, fn func() error) (bool, error) {
		return true, fn()
	}}
	// There are no held rows and no presence source; the complete pass still retires.
	require.NoError(t, proc.reconcileHolds(context.Background()))
	assert.Equal(t, model.CommandCancelled.String(), gateStatusOf(t, api, cmd.Token))
	other := core.WithTenant(context.Background(), "other")
	n, err := api.RetireCoveredCommands(other, 10)
	require.NoError(t, err)
	assert.Zero(t, n)
}
