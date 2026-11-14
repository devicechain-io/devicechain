// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCreateEntityRelationshipDeviceAuthoredOwnership(t *testing.T) {
	for _, kind := range []string{"assigned", "tenant-ownership"} {
		t.Run(kind, func(t *testing.T) {
			api := newPartialUpdateApi(t, &Device{}, &Customer{}, &EntityRelationship{}, &EntityRelationshipType{}, &PendingOwnershipFact{})
			ctx := partialUpdateCtx()
			device := &Device{}
			device.Token = "device"
			customer := &Customer{}
			customer.Token = "customer"
			require.NoError(t, api.RDB.DB(ctx).Create(device).Error)
			require.NoError(t, api.RDB.DB(ctx).Create(customer).Error)
			_, err := api.CreateEntityRelationshipType(ctx, &EntityRelationshipTypeCreateRequest{Token: kind, Tracked: true})
			require.NoError(t, err)
			request := &EntityRelationshipCreateRequest{Token: "edge", DeviceAuthored: true,
				SourceType: "device", Source: "device", TargetType: "customer", Target: "customer", RelationshipType: kind}
			edge, err := api.CreateEntityRelationship(ctx, request)
			require.ErrorIs(t, err, ErrDeviceAuthoredOwnership)
			require.Nil(t, edge)
			var n int64
			require.NoError(t, api.RDB.DB(ctx).Model(&EntityRelationship{}).Count(&n).Error)
			require.Zero(t, n)
			request.DeviceAuthored = false
			edge, err = api.CreateEntityRelationship(ctx, request)
			require.NoError(t, err)
			require.NotNil(t, edge)
		})
	}
}
