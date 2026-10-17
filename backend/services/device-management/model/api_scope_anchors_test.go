// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func scopeAnchorsApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Device{}, &EntityRelationship{}, &PendingOwnershipFact{}, &EntityRelationshipType{}, &EntityGroup{}, &EntityGroupMembership{}))
	return NewApi(&rdb.RdbManager{Database: db})
}

func seedAnchorDevice(t *testing.T, api *Api, ctx context.Context, tenant, token string) uint {
	t.Helper()
	d := Device{}
	d.Token, d.TenantId = token, tenant
	require.NoError(t, api.RDB.DB(ctx).Create(&d).Error)
	return d.ID
}

func seedRelType(t *testing.T, api *Api, ctx context.Context, tenant, token string, tracked bool) uint {
	t.Helper()
	rt := EntityRelationshipType{Tracked: tracked}
	rt.Token, rt.TenantId = token, tenant
	require.NoError(t, api.RDB.DB(ctx).Create(&rt).Error)
	return rt.ID
}

func seedEdge(t *testing.T, api *Api, ctx context.Context, tenant, token string, device, relType uint,
	targetType, targetToken string) *EntityRelationship {
	t.Helper()
	r := EntityRelationship{SourceType: string(entity.TypeDevice), SourceId: device, TargetType: targetType,
		TargetId: 1, TargetToken: targetToken, RelationshipTypeId: relType}
	r.Token, r.TenantId = token, tenant
	require.NoError(t, api.RDB.DB(ctx).Create(&r).Error)
	return &r
}

// A device's anchors are its TRACKED relationships, by type and token — whatever the type
// token is called — and nothing else: not an untracked edge, not a deleted one, not one that
// predates the denormalized target token, and never another tenant's edge on a device with
// the same token. A device nobody owns answers an empty list; a device that does not exist
// is absent.
func TestDeviceScopeAnchorsAreTheTrackedRelationshipsOnly(t *testing.T) {
	api := scopeAnchorsApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")

	d1 := seedAnchorDevice(t, api, acme, "acme", "d1")
	seedAnchorDevice(t, api, acme, "acme", "d2")
	assigned := seedRelType(t, api, acme, "acme", "assigned", true)
	operates := seedRelType(t, api, acme, "acme", "operates", true) // tracked, not "assigned"
	near := seedRelType(t, api, acme, "acme", "near", false)

	seedEdge(t, api, acme, "acme", "r1", d1, assigned, "customer", "acme-corp")
	seedEdge(t, api, acme, "acme", "r2", d1, operates, "customer", "initech")
	seedEdge(t, api, acme, "acme", "r3", d1, near, "customer", "globex") // untracked: not ownership
	seedEdge(t, api, acme, "acme", "r4", d1, assigned, "area", "north")
	seedEdge(t, api, acme, "acme", "r5", d1, assigned, "customer", "") // predates the token column
	gone := seedEdge(t, api, acme, "acme", "r6", d1, assigned, "customer", "former")
	require.NoError(t, api.RDB.DB(acme).Delete(gone).Error)

	// Another tenant: a device with the same token, owned by someone else.
	od := seedAnchorDevice(t, api, other, "other", "d1")
	ot := seedRelType(t, api, other, "other", "assigned", true)
	seedEdge(t, api, other, "other", "o1", od, ot, "customer", "evil-corp")

	got, err := api.DeviceScopeAnchors(acme, []string{"d1", "d2", "missing"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{
		"d1": {"customer:acme-corp", "customer:initech", "area:north"},
		"d2": {},
	}, got)

	got, err = api.DeviceScopeAnchors(other, []string{"d1"})
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"d1": {"customer:evil-corp"}}, got)
}

// The lookup is bounded like every other batch lookup.
func TestDeviceScopeAnchorsRefusesAnUnboundedList(t *testing.T) {
	api := scopeAnchorsApi(t)
	acme := core.WithTenant(context.Background(), "acme")
	tokens := make([]string, rdb.MaxLookupKeys+1)
	for i := range tokens {
		tokens[i] = "d"
	}
	_, err := api.DeviceScopeAnchors(acme, tokens)
	require.Error(t, err)
}
