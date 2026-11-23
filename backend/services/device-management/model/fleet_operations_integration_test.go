// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
)

// TestFleetOperationJournalsOnPostgres runs the appended migration through the real chain on a
// real server and asserts what the SQLite unit tests can only approximate: the CHECKs, the
// partial unique indexes and the driver_protocol default.
func TestFleetOperationJournalsOnPostgres(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	tenant := fmt.Sprintf("fleetjournal%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)

	op := &FleetOperation{Token: "t1", Kind: "config.set", Name: "n", Dispatch: "READY", RequestDigest: "sha256:x",
		Intent: "{}", TargetKind: "DEVICE_LIST", TargetCount: 1, MaxPostsPerSecond: 10, EffPostsPerSecond: 10,
		SettleDeadlineS: 60, Initiator: "u"}
	require.NoError(t, db.WithContext(ctx).Create(op).Error)
	var stored FleetOperation
	require.NoError(t, db.WithContext(ctx).First(&stored, op.ID).Error)
	require.Equal(t, int32(1), stored.DriverProtocol, "an operation this release creates is protocol 1")
	require.Equal(t, "OPEN", stored.Evidence)

	dup := *op
	dup.ID = 0
	require.Error(t, db.WithContext(ctx).Create(&dup).Error, "(tenant, token) is unique")

	bad := *op
	bad.ID, bad.Token, bad.Dispatch = 0, "t2", "DONE"
	require.Error(t, db.WithContext(ctx).Create(&bad).Error, "dispatch is a closed vocabulary")
	require.Error(t, db.WithContext(ctx).Exec(`UPDATE fleet_operations SET driver_protocol = 0 WHERE id = ?`, op.ID).Error)

	id := int64(op.ID)
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 1, Posting: "PLANNED"}).Error)
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 2, Posting: "PLANNED", Prior: sql.NullString{String: "{}", Valid: true}}).Error,
		"a before-image exists only on a posted line")
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 3, Posting: "REFUSED", Settlement: sql.NullString{String: "FAILED", Valid: true}}).Error,
		"a settlement exists only on a posted line")
	require.NoError(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 4, Posting: "POSTED", Settlement: sql.NullString{String: "AMBIGUOUS", Valid: true}}).Error)
	require.Error(t, db.WithContext(ctx).Create(&FleetOperationLine{OperationId: id, Ordinal: 4, Posting: "PLANNED"}).Error, "one line per ordinal")
}
