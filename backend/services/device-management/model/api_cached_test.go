// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newCachedApiForTest stands up a CachedApi over sqlite with an in-memory relationships
// cache, and returns the store so a test can see what the cache was actually asked to do.
func newCachedApiForTest(t *testing.T) (*CachedApi, *msgtest.MemoryKV, context.Context, uint) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, rdb.RegisterTenantScoping(db), "register tenant scoping")
	require.NoError(t, db.AutoMigrate(&Device{}, &DeviceType{}, &DeviceProfile{},
		&EntityRelationship{}, &EntityRelationshipType{}), "migrate")

	api := NewApi(&rdb.RdbManager{Database: db})
	ctx := core.WithTenant(context.Background(), "acme")

	device := Device{}
	device.Token = "dev1"
	device.TenantId = "acme"
	require.NoError(t, api.RDB.DB(ctx).Create(&device).Error, "seed device")

	relType := EntityRelationshipType{Tracked: true}
	relType.Token = "tracks"
	relType.TenantId = "acme"
	require.NoError(t, api.RDB.DB(ctx).Create(&relType).Error, "seed relationship type")

	rel := EntityRelationship{
		SourceType:         string(entity.TypeDevice),
		SourceId:           device.ID,
		TargetType:         string(entity.TypeArea),
		TargetId:           1,
		RelationshipTypeId: relType.ID,
	}
	rel.Token = "rel1"
	rel.TenantId = "acme"
	require.NoError(t, api.RDB.DB(ctx).Create(&rel).Error, "seed relationship")

	kv := msgtest.NewMemoryKV()
	return NewCachedApi(api, &Caches{RelationshipsBySource: kv.NewCache()}), kv, ctx, device.ID
}

// The cached relationship read is actually served from the cache on a repeat call.
//
// 🔴🔴 THIS IS THE FIRST TEST OF CachedApi IN THE REPOSITORY, and the gap it closes is
// specific: CachedApi embeds *Api, so every method it does not declare is PROMOTED. An
// override that is renamed or deleted therefore still compiles, still returns correct
// rows, and still passes every existing test — it just quietly stops caching, on the
// inbound event-resolution hot path this decorator exists for.
//
// 🔑 SO THE ASSERTION IS ON THE CACHE TRAFFIC, NOT THE RESULT. Both the cached and the
// uncached method return the same relationships, which is exactly why comparing values
// cannot detect a lost override. What separates them is that one of them TOUCHES the
// cache, so the double counts and the test reads the counts.
func TestTheCachedRelationshipReadIsServedFromCacheOnARepeatCall(t *testing.T) {
	capi, kv, ctx, deviceId := newCachedApiForTest(t)

	first, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err, "first read")
	require.Len(t, first.Results, 1, "the seeded tracked relationship must come back")

	require.Equal(t, 1, kv.Gets, "the first read did not consult the cache at all, which is "+
		"what a lost override looks like: the call fell through to the embedded plain Api")
	require.Equal(t, 1, kv.Puts, "the first read did not populate the cache, so every later "+
		"read would go to the database")

	second, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err, "second read")
	require.Len(t, second.Results, 1, "the cached read must return the same set")

	require.Equal(t, 2, kv.Gets, "the second read did not consult the cache")
	require.Equal(t, 1, kv.Puts, "the second read wrote to the cache again, so it was a MISS: "+
		"the entry is being stored under a key the read does not look under, and the cache "+
		"never serves anything")
}

// 🔑 THE PROOF THAT THE SECOND READ CAME FROM THE CACHE RATHER THAN THE DATABASE. The
// counter assertions above show the cache was consulted; they cannot by themselves show
// the DB was skipped. Deleting the row between the two reads separates those: a read that
// still returns it can only have got it from the cache.
func TestACachedRelationshipSurvivesTheRowBeingDeleted(t *testing.T) {
	capi, _, ctx, deviceId := newCachedApiForTest(t)

	first, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err, "first read")
	require.Len(t, first.Results, 1)

	require.NoError(t, capi.Api.RDB.DB(ctx).Unscoped().
		Where("source_id = ?", deviceId).Delete(&EntityRelationship{}).Error, "delete the row")

	second, err := capi.TrackedRelationshipsForDevice(ctx, deviceId)
	require.NoError(t, err, "second read")
	require.Len(t, second.Results, 1, "the second read went to the database (the row is gone), "+
		"so the cache was not serving it")
}

// With no tenant in context the cache is bypassed entirely rather than shared across
// tenants — a cross-tenant cache hit would be a tenant-isolation breach.
func TestTheRelationshipCacheIsBypassedWithNoTenantInContext(t *testing.T) {
	capi, kv, _, deviceId := newCachedApiForTest(t)

	_, err := capi.TrackedRelationshipsForDevice(context.Background(), deviceId)
	require.Error(t, err, "a tenant-scoped read with no tenant must fail closed")
	require.Equal(t, 0, kv.Gets, "the cache was consulted with no tenant to key it by")
	require.Equal(t, 0, kv.Puts, "an entry was written with no tenant in the key")
}
