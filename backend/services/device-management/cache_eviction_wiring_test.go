// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔑 THE TIME TO LIVE IS MINUTES, SO A CHANGE MADE THROUGH ONE REPLICA MUST REACH EVERY OTHER
// REPLICA'S MEMORY BY MESSAGE, NOT BY EXPIRY. These tests build each replica as the service
// does (buildApis over a real broker) and write through the PLAIN Api, which is what the
// GraphQL mutations call. Replica b reads through its cached decorator, as the resolver
// does, and must see the change within a second, which is a tiny fraction of the five
// minutes its copy would otherwise live.
//
// The control replica sits on a broker of its own and shares the database. It is the same
// code with the broadcast path cut: it must STILL serve the old answer, which is what shows
// these tests can tell a delivered eviction from something else (a re-read, an expiry).

func evictionTables() []any {
	return append(credentialTables(), &model.DeviceReplacement{}, &model.EntityRelationship{},
		&model.EntityRelationshipType{}, &model.EntityAttribute{}, &model.Alarm{},
		&model.EntityGroupMembership{}, &model.GeoFence{}, &model.GeoFenceSetVersion{},
		&model.GeoFenceGeometryBlob{})
}

// evictionRig is replicas a (the writer), b (the reader) and a control on a broker of its
// own, over one database, with tenant acme's context.
type evictionRig struct {
	a, b, control replica
	ctx           context.Context
	db            *gorm.DB
}

func newEvictionRig(t *testing.T) *evictionRig {
	t.Helper()
	host, port := startEmbeddedNats(t)
	isolatedHost, isolatedPort := startEmbeddedNats(t)
	instance := fmt.Sprintf("evictwiring%d", time.Now().UnixNano())
	db := putest.NewSQLiteDB(t, evictionTables()...)
	r := &evictionRig{
		a:       newReplica(t, host, port, instance, db),
		b:       newReplica(t, host, port, instance, db),
		control: newReplica(t, isolatedHost, isolatedPort, instance, db),
		ctx:     core.WithTenant(context.Background(), "acme"),
		db:      db,
	}
	return r
}

func (r *evictionRig) each() map[string]replica {
	return map[string]replica{"a": r.a, "b": r.b, "control": r.control}
}

// eventually polls cond for up to a second, which is far inside the minutes a copy lives.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("within 1s: %s", what)
}

func (r *evictionRig) seedDevices(t *testing.T) (dev, dev2 *model.Device) {
	t.Helper()
	api := r.a.api
	for _, tok := range []string{"t1", "t2"} {
		_, err := api.CreateDeviceType(r.ctx, &model.DeviceTypeCreateRequest{Token: tok})
		require.NoError(t, err)
	}
	dev, err := api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "t1"})
	require.NoError(t, err)
	dev2, err = api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: "dev2", DeviceTypeToken: "t1"})
	require.NoError(t, err)
	return dev, dev2
}

func typeOf(t *testing.T, ctx context.Context, r replica, token string) uint {
	t.Helper()
	got, err := r.capi.DevicesByToken(ctx, []string{token})
	require.NoError(t, err)
	if len(got) != 1 {
		return 0
	}
	return got[0].DeviceTypeId
}

// DeviceByToken: a re-type, and a delete, made through the plain Api (the GraphQL path) on
// replica a reach replica b's cached lookup at once. The re-type is the one that, before
// this, evicted nothing at all: the only override that did sat on the decorator the
// mutation does not use.
func TestADeviceChangeReachesAnotherReplicasByTokenCache(t *testing.T) {
	r := newEvictionRig(t)
	dev, _ := r.seedDevices(t)
	before := dev.DeviceTypeId
	for name, rep := range r.each() {
		require.Equal(t, before, typeOf(t, r.ctx, rep, "dev"), "warm %s", name)
	}

	_, err := r.a.api.UpdateDevice(r.ctx, "dev", &model.DeviceUpdateRequest{
		DeviceTypeToken: dcgraphql.OptionalStringOf("t2")})
	require.NoError(t, err)

	for _, name := range []string{"a", "b"} {
		rep := r.each()[name]
		eventually(t, name+" kept resolving the device under its old type", func() bool {
			got := typeOf(t, r.ctx, rep, "dev")
			return got != 0 && got != before
		})
	}
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, before, typeOf(t, r.ctx, r.control, "dev"),
		"the control replica, which hears no broadcast, re-read: the test cannot tell the broadcast from something else")

	deleted, err := r.a.api.DeleteDevice(r.ctx, "dev")
	require.NoError(t, err)
	require.True(t, deleted)
	for _, name := range []string{"a", "b"} {
		rep := r.each()[name]
		eventually(t, name+" kept resolving a deleted device", func() bool {
			return typeOf(t, r.ctx, rep, "dev") == 0
		})
	}
}

func trackedCount(t *testing.T, ctx context.Context, r replica, deviceId uint) int {
	t.Helper()
	res, err := r.capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err)
	return len(res.Results)
}

// RelationshipsBySource: creating an edge, removing it, bulk-creating, and flipping its
// type's tracked flag, each made through the plain Api on replica a, reach replica b's cached
// set at once. Create and the type update evicted nothing before.
func TestAnEdgeChangeReachesAnotherReplicasTrackedSetCache(t *testing.T) {
	r := newEvictionRig(t)
	dev, dev2 := r.seedDevices(t)
	_, err := r.a.api.CreateEntityRelationshipType(r.ctx, &model.EntityRelationshipTypeCreateRequest{
		Token: "watches", Tracked: true})
	require.NoError(t, err)
	edge := func(token string) *model.EntityRelationshipCreateRequest {
		return &model.EntityRelationshipCreateRequest{Token: token, SourceType: "device", Source: "dev",
			TargetType: "device", Target: "dev2", RelationshipType: "watches"}
	}
	for name, rep := range r.each() {
		require.Zero(t, trackedCount(t, r.ctx, rep, dev.ID), "warm %s", name)
	}
	waitB := func(what string, want int) {
		t.Helper()
		eventually(t, what, func() bool { return trackedCount(t, r.ctx, r.b, dev.ID) == want })
	}

	_, err = r.a.api.CreateEntityRelationship(r.ctx, edge("e1"))
	require.NoError(t, err)
	waitB("b did not see the new edge", 1)
	require.Zero(t, trackedCount(t, r.ctx, r.control, dev.ID),
		"the control replica saw the edge without a broadcast: the test cannot tell the broadcast from a re-read")

	removed, err := r.a.api.RemoveEntityRelationship(r.ctx, "e1")
	require.NoError(t, err)
	require.True(t, removed)
	waitB("b kept a removed edge", 0)

	_, err = r.a.api.CreateEntityRelationships(r.ctx, []*model.EntityRelationshipCreateRequest{edge("e2")})
	require.NoError(t, err)
	waitB("b did not see the bulk-created edge", 1)

	_, err = r.a.api.UpdateEntityRelationshipType(r.ctx, "watches", &model.EntityRelationshipTypeUpdateRequest{
		Tracked: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	waitB("b kept serving an edge whose type stopped being tracked", 0)

	// Deleting the target removes the edge and evicts the tracking device's set.
	_, err = r.a.api.UpdateEntityRelationshipType(r.ctx, "watches", &model.EntityRelationshipTypeUpdateRequest{
		Tracked: dcgraphql.OptionalBoolOf(true)})
	require.NoError(t, err)
	waitB("b did not see the edge once its type was tracked again", 1)
	deleted, err := r.a.api.DeleteDevice(r.ctx, "dev2")
	require.NoError(t, err)
	require.True(t, deleted)
	waitB("b kept the edge to a deleted target", 0)
	_ = dev2
}

// ProfileResolutionByType: a publish through replica a reaches replica b's cached resolution
// at once. (The same triggers are pinned one by one against a single replica in the model
// package; this shows the eviction crosses replicas.)
func TestAPublishReachesAnotherReplicasProfileResolutionCache(t *testing.T) {
	r := newEvictionRig(t)
	api := r.a.api
	_, err := api.CreateDeviceProfile(r.ctx, &model.DeviceProfileCreateRequest{Token: "p"})
	require.NoError(t, err)
	unit := "Cel"
	_, err = api.CreateMetricDefinition(r.ctx, &model.MetricDefinitionCreateRequest{
		Token: "temp-def", DeviceProfileToken: "p", MetricKey: "temp", DataType: "DOUBLE", Unit: &unit})
	require.NoError(t, err)
	_, err = r.a.capi.PublishDeviceProfile(r.ctx, "p", nil, nil, "t")
	require.NoError(t, err)
	profile := "p"
	dt, err := api.CreateDeviceType(r.ctx, &model.DeviceTypeCreateRequest{Token: "dt", ProfileToken: &profile})
	require.NoError(t, err)

	version := func(rep replica) string {
		res, err := rep.capi.ProfileResolutionByDeviceType(r.ctx, dt.ID)
		require.NoError(t, err)
		return res.Scope.ProfileVersionToken
	}
	for name, rep := range r.each() {
		require.Equal(t, "p@1", version(rep), "warm %s", name)
	}

	_, err = r.a.capi.PublishDeviceProfile(r.ctx, "p", nil, nil, "t")
	require.NoError(t, err)

	eventually(t, "b kept stamping events with the previous profile version", func() bool {
		return version(r.b) == "p@2"
	})
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, "p@1", version(r.control),
		"the control replica, which hears no broadcast, re-read: the test cannot tell the broadcast from something else")
}

// Every cache InitializeCaches builds drops an entry on another replica when it is deleted
// on this one, with the keys the service files them under. It is the pin on the wiring
// itself: a cache built without WithCrossReplicaEviction passes every other test and serves
// a deleted entry for the whole time to live.
func TestEveryCacheEvictsOnOtherReplicas(t *testing.T) {
	r := newEvictionRig(t)
	caches := func(rep replica) map[string]*messaging.Cache {
		c := rep.capi.Caches()
		return map[string]*messaging.Cache{
			model.CACHE_NAME_DEVICE_BY_TOKEN:            c.DeviceByToken,
			model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    c.RelationshipsBySource,
			model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: c.ProfileResolutionByType,
			model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      c.MembershipsByEntity,
			model.CACHE_NAME_SCOPED_GROUPS_EXIST:        c.ScopedGroupsExist,
		}
	}
	ca, cb := caches(r.a), caches(r.b)
	for name := range ca {
		key := "acme|k-" + name
		require.NoError(t, ca[name].Set(r.ctx, key, "v"), name)
		var got string
		found, err := cb[name].Get(r.ctx, key, &got)
		require.NoError(t, err, name)
		require.True(t, found && got == "v", "%s: replica b did not read what a stored", name)

		require.NoError(t, ca[name].Delete(r.ctx, key), name)
		name := name
		eventually(t, name+": replica b kept serving an entry replica a deleted", func() bool {
			var v string
			found, _ := cb[name].Get(r.ctx, key, &v)
			return !found
		})
	}
}

// A tenant's erasure drops that tenant's entries from every cache on every replica, the
// credential cache included, and leaves another tenant's alone.
func TestATenantErasureEmptiesEveryReplicasCaches(t *testing.T) {
	r := newEvictionRig(t)
	dev, _ := r.seedDevices(t)
	secret := "s3cret"
	_, err := r.a.api.CreateDeviceCredential(r.ctx, &model.DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true})
	require.NoError(t, err)
	stmts := countCredentialStatements(t, r.db)
	now := time.Now()

	require.Equal(t, dev.DeviceTypeId, typeOf(t, r.ctx, r.b, "dev"))
	_, err = r.b.capi.AuthenticateDevice(r.ctx, basicCred(), now)
	require.NoError(t, err)
	stmts.n.Store(0)
	_, err = r.b.capi.AuthenticateDevice(r.ctx, basicCred(), now)
	require.NoError(t, err)
	require.Zero(t, stmts.n.Load(), "replica b holds no credential copy, so the test proves nothing")

	// Another tenant's entry, which must survive.
	other := core.WithTenant(context.Background(), "beta")
	require.NoError(t, r.b.capi.Caches().DeviceByToken.Set(other, "beta|dev", "v"))

	nc := r.a.nmgr.Conn()
	require.NoError(t, messaging.BroadcastTenantCacheEviction(nc, r.a.nmgr.Microservice.InstanceId, "acme"))

	eventually(t, "replica b kept authenticating a credential of an erased tenant from memory", func() bool {
		stmts.n.Store(0)
		_, err := r.b.capi.AuthenticateDevice(r.ctx, basicCred(), now)
		return err == nil && stmts.n.Load() > 0
	})
	var v string
	found, err := r.b.capi.Caches().DeviceByToken.Get(other, "beta|dev", &v)
	require.NoError(t, err)
	require.True(t, found, "another tenant's entry was evicted by acme's erasure")
}
