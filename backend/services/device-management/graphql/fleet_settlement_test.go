// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/require"
)

func TestEvidenceCapabilityReadsTargetCapture(t *testing.T) {
	ctx, api := fleetWireCtx(t, &auth.Claims{Username: "reader"})
	db := api.RDB.DB(ctx)
	var device model.Device
	require.NoError(t, db.Where("token=?", "d1").First(&device).Error)
	op := model.FleetOperation{Token: "capture", Kind: "config.set", Dispatch: "READY", DriverProtocol: 1, Intent: "{}", BrokerFailClosed: true}
	require.NoError(t, db.Create(&op).Error)
	require.NoError(t, db.Create(&model.FleetOperationLine{OperationId: int64(op.ID), Ordinal: 1, Posting: "PLANNED", TargetId: sql.NullInt64{Int64: int64(device.ID), Valid: true}}).Error)
	resolver := FleetOperationResolver{M: op, S: &SchemaResolver{}, C: ctx}
	capability, err := resolver.EvidenceCapability()
	require.NoError(t, err)
	require.False(t, capability.ReportLog())
	require.Contains(t, capability.Note(), "incomplete")
	state := model.DeviceConfigurationState{DeviceId: device.ID, CaptureCompleteSince: sql.NullTime{Time: time.Now().Add(-time.Hour), Valid: true}}
	require.NoError(t, db.Create(&state).Error)
	capability, err = resolver.EvidenceCapability()
	require.NoError(t, err)
	require.True(t, capability.ReportLog())
	require.True(t, capability.BrokerFailClosed())
	require.NoError(t, db.Model(&state).Update("capture_complete_since", nil).Error)
	capability, err = resolver.EvidenceCapability()
	require.NoError(t, err)
	require.False(t, capability.ReportLog())
	require.Contains(t, capability.Note(), "incomplete")
}
