// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 🔑 A DEVICE CLAIM WRITES AND DELETES DEVICE-SOURCED EDGES, so it changes the device's
// cached tracked set: redeeming one adds the new owner's edge, and reopening a claimed one
// (a resale) removes the previous owner's. The cached set outlives either change for the
// whole time to live unless the write evicts it.
func TestAClaimEvictsTheDevicesTrackedSet(t *testing.T) {
	api, ctx := hierarchyTestApi(t)
	seedDeviceForHierarchy(t, api, ctx, "dozer-01")
	devices, err := api.DevicesByToken(ctx, []string{"dozer-01"})
	require.NoError(t, err)
	deviceId := devices[0].ID

	customerType := &CustomerType{}
	customerType.Token = "operator"
	require.NoError(t, api.RDB.DB(ctx).Create(customerType).Error)
	_, err = api.CreateCustomer(ctx, &CustomerCreateRequest{Token: "acme-mining", CustomerTypeToken: "operator"})
	require.NoError(t, err)
	assignment, err := api.EnsureAssignmentType(ctx)
	require.NoError(t, err)

	evictor := &captureEvictor{}
	api.CacheEvictor = evictor

	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret"})
	require.NoError(t, err)
	require.Empty(t, evictor.relSources, "opening a claim writes no edge, so it evicts nothing")

	_, err = api.ClaimDevice(ctx, &DeviceClaimRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret",
		CustomerToken: "acme-mining", RelationshipType: assignment.Token}, time.Now())
	require.NoError(t, err)
	require.Equal(t, [][]uint{{deviceId}}, evictor.relSources,
		"redeeming a claim added an edge from the device but did not evict its cached set")

	_, err = api.InitiateDeviceClaim(ctx, &DeviceClaimInitiateRequest{DeviceToken: "dozer-01", ClaimSecret: "s3cret-2"})
	require.NoError(t, err)
	require.Equal(t, [][]uint{{deviceId}, {deviceId}}, evictor.relSources,
		"reopening a claimed device removed the previous owner's edge but did not evict the cached set")
}
