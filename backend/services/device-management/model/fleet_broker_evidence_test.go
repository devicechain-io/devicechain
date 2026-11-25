// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

func TestFleetStartSnapshotsBrokerConfigAtStart(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 1)
			cfg := &config.NatsConfiguration{MqttFailClosed: !on}
			api.FleetNatsConfig = cfg
			op := readyOp(t, api, ctx, "snapshot", FleetSafetyRequest{}, "d1")
			require.False(t, op.BrokerFailClosed, "CREATE does not snapshot the capability")
			cfg.MqttFailClosed = on
			// START must write false too, rather than retain an earlier value.
			require.NoError(t, api.RDB.DB(ctx).Model(op).Update("broker_fail_closed", !on).Error)
			started, err := api.StartFleetOperation(ctx, op.Token, op.ContractDigest, alice)
			require.NoError(t, err)
			require.Equal(t, on, started.BrokerFailClosed)
			stored, err := api.fleetOperationByToken(ctx, op.Token)
			require.NoError(t, err)
			require.Equal(t, on, stored.BrokerFailClosed)
			require.Equal(t, op.ContractDigest, stored.ContractDigest)
			require.Equal(t, FleetDispatchRunning, stored.Dispatch)
		})
	}
}

func TestFleetResumeOnlyDowngradesBrokerEvidence(t *testing.T) {
	for _, initial := range []bool{true, false} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			armed(t)
			api, ctx := fleetFixture(t, 2)
			cfg := &config.NatsConfiguration{MqttFailClosed: initial}
			api.FleetNatsConfig = cfg
			op := running(t, api, ctx, "resume", FleetSafetyRequest{})
			// Seed independently of START so this test isolates RESUME's guarantee.
			require.NoError(t, api.RDB.DB(ctx).Model(op).Update("broker_fail_closed", initial).Error)
			want := initial
			for _, nowOn := range []bool{true, false, true, false} {
				cfg.MqttFailClosed = nowOn
				paused, err := api.PauseFleetOperation(ctx, op.Token, alice.Name)
				require.NoError(t, err)
				require.Equal(t, want, paused.BrokerFailClosed, "PAUSE does not resnapshot")
				resumed, err := api.ResumeFleetOperation(ctx, op.Token, op.ContractDigest, nil, alice)
				require.NoError(t, err)
				want = want && nowOn
				require.Equal(t, want, resumed.BrokerFailClosed)
				stored, err := api.fleetOperationByToken(ctx, op.Token)
				require.NoError(t, err)
				require.Equal(t, want, stored.BrokerFailClosed)
				require.Equal(t, FleetDispatchRunning, stored.Dispatch)
				require.Equal(t, op.ContractDigest, stored.ContractDigest)
			}
		})
	}
}

func TestFleetBrokerEvidenceDoesNotChangeDispatchOrSettlement(t *testing.T) {
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprint(on), func(t *testing.T) {
			e := driveFixture(t, 3, FleetSafetyRequest{})
			e.api.FleetNatsConfig = &config.NatsConfiguration{MqttFailClosed: on}
			db := e.api.RDB.DB(e.ctx)
			require.NoError(t, db.Model(e.op).Update("broker_fail_closed", on).Error)
			var first FleetOperationLine
			require.NoError(t, db.Where("operation_id = ?", e.op.ID).Order("ordinal").First(&first).Error)
			// Settlement reconciliation has not shipped yet. Exercise the stored verdict
			// and outstanding evidence that current control/dispatch/progress paths consume.
			require.NoError(t, db.Model(&first).Updates(map[string]any{
				"posting": FleetPostingPosted, "settlement": string(ledger.SettlementAmbiguous),
			}).Error)
			_, err := e.api.PauseFleetOperation(e.ctx, e.op.Token, alice.Name)
			require.NoError(t, err)
			_, err = e.api.ResumeFleetOperation(e.ctx, e.op.Token, e.op.ContractDigest, nil, alice)
			require.NoError(t, err)
			r := e.drive(t)
			require.Equal(t, &FleetDriveResult{Posted: 2, Dispatched: true}, r)
			op := e.current(t)
			require.Equal(t, on, op.BrokerFailClosed)
			require.Equal(t, FleetDispatchDispatched, op.Dispatch)
			require.Equal(t, FleetEvidenceOpen, op.Evidence)
			p, err := e.api.FleetOperationProgressOf(e.ctx, op)
			require.NoError(t, err)
			require.Equal(t, &FleetOperationProgress{Posted: 3, Ambiguous: 1, AwaitingEvidence: 2}, p)
			require.Equal(t, string(ledger.SettlementAmbiguous), e.reloadLine(t, first.ID).Settlement.String)
		})
	}
}
