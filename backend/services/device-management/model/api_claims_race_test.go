// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func claimRaceFixture(t *testing.T) (*Api, context.Context) {
	t.Helper()
	api, ctx := hierarchyTestApi(t)
	seedDeviceForHierarchy(t, api, ctx, "race-device")
	ct := &CustomerType{}
	ct.Token = "race-customer-type"
	require.NoError(t, api.RDB.DB(ctx).Create(ct).Error)
	_, err := api.CreateCustomer(ctx, &CustomerCreateRequest{Token: "race-customer", CustomerTypeToken: ct.Token})
	require.NoError(t, err)
	_, err = api.EnsureAssignmentType(ctx)
	require.NoError(t, err)
	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "race-device", ClaimSecret: "old-secret"})
	require.NoError(t, err)
	return api, ctx
}

// Run a real redemption after the first claim read has populated its destination,
// including preloads, but before the caller can use that snapshot to write.
func redeemAfterClaimRead(t *testing.T, api *Api, ctx context.Context) *bool {
	t.Helper()
	fired := false
	queries := api.RDB.Database.Callback().Query()
	const name = "test:redeem_after_claim_read"
	require.NoError(t, queries.After("gorm:after_query").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "device_claims" {
			return
		}
		fired = true
		_, err := api.ClaimDevice(ctx, &DeviceClaimRequest{
			DeviceToken: "race-device", ClaimSecret: "old-secret", CustomerToken: "race-customer",
		}, time.Now())
		require.NoError(t, err)
	}))
	t.Cleanup(func() { require.NoError(t, queries.Remove(name)) })
	return &fired
}

func TestCancelDeviceClaimRacingRedemption(t *testing.T) {
	api, ctx := claimRaceFixture(t)
	fired := redeemAfterClaimRead(t, api, ctx)
	canceled, err := api.CancelDeviceClaim(ctx, "race-device")
	require.NoError(t, err)
	require.True(t, *fired)
	claim, err := api.DeviceClaimByDeviceToken(ctx, "race-device")
	require.NoError(t, err)
	require.Equal(t, string(ClaimStatusClaimed), claim.Status)
	require.False(t, canceled)
	var edges int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).
		Where("source_type = ? AND source_id = ? AND target_type = ?", "device", claim.DeviceId, "customer").Count(&edges).Error)
	require.EqualValues(t, 1, edges, "cancel must preserve the redeeming customer's ownership")
}

func TestReinitiateDeviceClaimRacingRedemption(t *testing.T) {
	api, ctx := claimRaceFixture(t)
	fired := redeemAfterClaimRead(t, api, ctx)
	claim, err := api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "race-device", ClaimSecret: "new-secret"})
	require.NoError(t, err)
	require.True(t, *fired)
	require.Equal(t, string(ClaimStatusOpen), claim.Status)
	require.False(t, claim.ClaimedByCustomerId.Valid)
	stored, err := api.DeviceClaimByDeviceToken(ctx, "race-device")
	require.NoError(t, err)
	require.Equal(t, string(ClaimStatusOpen), stored.Status)
	var edges int64
	require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).
		Where("source_type = ? AND source_id = ? AND target_type = ?", "device", claim.DeviceId, "customer").Count(&edges).Error)
	require.Zero(t, edges, "reopening must revoke the owner who redeemed after the initial read")
}

// SQLite ignores FOR UPDATE. Inject a change on the transaction connection after
// its read to exercise each half of the optimistic guard without lock contention.
func TestReinitiateDeviceClaimRefusesChangedSnapshot(t *testing.T) {
	for _, column := range []string{"status", "updated_at"} {
		t.Run(column, func(t *testing.T) {
			api, ctx := claimRaceFixture(t)
			_, err := api.ClaimDevice(ctx, &DeviceClaimRequest{
				DeviceToken: "race-device", ClaimSecret: "old-secret", CustomerToken: "race-customer",
			}, time.Now())
			require.NoError(t, err)
			original, err := api.DeviceClaimByDeviceToken(ctx, "race-device")
			require.NoError(t, err)
			fired := false
			updates := api.RDB.Database.Callback().Update()
			const name = "test:change_claim_before_write"
			require.NoError(t, updates.Before("gorm:update").Register(name, func(tx *gorm.DB) {
				if fired || tx.Statement.Table != "device_claims" {
					return
				}
				fired = true
				var value interface{} = string(ClaimStatusOpen)
				if column == "updated_at" {
					value = original.UpdatedAt.Add(time.Hour)
				}
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&DeviceClaim{}).
					Where("id = ?", original.ID).UpdateColumn(column, value).Error)
			}))
			t.Cleanup(func() { require.NoError(t, updates.Remove(name)) })
			_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "race-device", ClaimSecret: "new-secret"})
			require.True(t, fired)
			require.ErrorIs(t, err, ErrClaimNotOpen)
			stored, err := api.DeviceClaimByDeviceToken(ctx, "race-device")
			require.NoError(t, err)
			require.Equal(t, original.ClaimSecretDigest, stored.ClaimSecretDigest, "a refused reopen must roll back")
			var edges int64
			require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).
				Where("source_type = ? AND source_id = ? AND target_type = ?", "device", original.DeviceId, "customer").Count(&edges).Error)
			require.EqualValues(t, 1, edges, "a refused reopen must roll back the ownership revoke")
		})
	}
}

// A cancel that lands is journaled against the claim row it changed: the statement is
// built on the loaded claim, so the audit entry names the row rather than recording an
// anonymous update to device_claims.
func TestCancelDeviceClaimIsJournaledAgainstTheClaimRow(t *testing.T) {
	api, ctx := claimRaceFixture(t)
	sys := core.WithSystemContext(context.Background())
	require.NoError(t, api.RDB.Database.WithContext(sys).AutoMigrate(&rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	claim, err := api.DeviceClaimByDeviceToken(ctx, "race-device")
	require.NoError(t, err)

	canceled, err := api.CancelDeviceClaim(ctx, "race-device")
	require.NoError(t, err)
	require.True(t, canceled)

	var rows []rdb.AuditEvent
	require.NoError(t, api.RDB.DB(sys).
		Where("table_name = ? AND operation = ?", "device_claims", "update").Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, fmt.Sprint(claim.ID), rows[0].EntityPK, "the journal must name the canceled claim")
}
