// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// Each cache name is its entry in the core kv inventory, which is what selects
// the bucket's disk ceiling (ADR-023). Naming them here rather than repeating the
// literals means a rename breaks the build instead of silently detaching a bucket
// from its ceiling and dropping it out of the disk budget.
const (
	CACHE_NAME_DEVICE_BY_TOKEN            = kv.BucketDeviceByToken
	CACHE_NAME_RELATIONSHIPS_BY_SOURCE    = kv.BucketRelationshipsBySource
	CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE = kv.BucketProfileResolutionByType
	CACHE_NAME_MEMBERSHIPS_BY_ENTITY      = kv.BucketMembershipsByEntity
	CACHE_NAME_SCOPED_GROUPS_EXIST        = kv.BucketScopedGroupsExist
)

// Caches bundles the caches the cached API decorator reads from and evicts: the
// lookups the hot inbound-event resolution path repeats on every event (ADR-022
// review B2) — the device by token, its tracked relationships, its type's published
// profile, and the rule-scoped group reads. Everything else falls through to the DB.
type Caches struct {
	// DeviceByToken caches positive device-by-token lookups (keyed by tenant+token).
	DeviceByToken *messaging.Cache
	// RelationshipsBySource caches a device's tracked relationships (keyed by
	// tenant+source device id).
	RelationshipsBySource *messaging.Cache
	// ProfileResolutionByType caches a device type's ProfileResolution — ONE entry per
	// (tenant, device type) holding the active published profile version's metric
	// definitions (ADR-016/045) together with the rule-scoping identity and fence-set
	// version stamped onto every resolved event (ADR-051/078). It is one entry, read
	// once per event, so an event's validation, classifier stamp and version token all
	// come from the same version.
	//
	// Three triggers evict it: a type's profile pointer changing (UpdateDeviceType), the
	// profile being published or rolled back (evictProfileResolution), and the tenant
	// minting a fence-set version (EvictFenceSetVersion). A profile-TOKEN rename would be
	// a fourth, since the version token embeds the profile token — but
	// renameDeviceProfile refuses a profile that has been published or adopted, so no
	// reachable rename can move a token this cache has an entry for (see the note where
	// that override used to live, in api_cached.go).
	// Empty resolutions (untyped/unpublished) are cached too so a device with no rules
	// and no declared metrics does not query on every event.
	ProfileResolutionByType *messaging.Cache
	// MembershipsByEntity caches the rule-scoped dynamic-group versions an entity
	// belongs to (ADR-062), keyed by tenant+entityType+entityId, read on the hot
	// resolve path to stamp scope memberships onto an event. Empty results are cached
	// (a non-member is the common case); the entry is explicitly evicted on every
	// membership mutation, with the TTL as a self-healing backstop.
	MembershipsByEntity *messaging.Cache
	// ScopedGroupsExist caches, per tenant, whether ANY rule-scoped group exists
	// (ADR-062 Decision 7) — the resolver's pay-nothing gate: a tenant with no scoped
	// group does zero per-entity membership reads. Evicted on register/deregister/
	// group-delete; the TTL is a backstop.
	ScopedGroupsExist *messaging.Cache
	// Credentials keeps a device credential that has just verified, with its device, in
	// process memory for inMemoryCache.ttlSeconds, in front of AuthenticateDevice. Nil means
	// credentials are not cached (unit fixtures).
	//
	// 🔴 IT IS NEVER A KEY-VALUE BUCKET: an entry holds an MQTT_BASIC password, and an
	// access token's id IS its secret, so nothing of it may reach broker storage. Replicas
	// drop each other's entries through a messaging.EvictionBroadcast instead of a bucket.
	Credentials *CredentialCache
}

// InitializeCaches builds the caches used by the cached API, TTL'd from the
// service configuration (ADR-022 decision 1) and backed by NATS JetStream KV
// (ADR-007: NATS KV cache backend). The returned bundle is held by CachedApi so
// it can serve, populate, and evict entries.
//
// 🔑 EVICT-ON-CHANGE IS THE MECHANISM, THE TIME TO LIVE IS THE BACKSTOP, AND THAT IS WHAT
// LETS THE TIME BE MINUTES. Every one of these keeps an in-process tier for
// cfg.InMemoryCache.TtlSeconds (5 minutes by default; a bucket-backed one never longer than
// its bucket's own time), and every one is built WithCrossReplicaEviction: a Delete clears
// the bucket (shared by every replica), drops the entry from this replica's memory, and
// broadcasts the key so every OTHER replica drops it from theirs. The credential cache has no
// bucket and does the same through its own broadcast. A replica that reconnects to the broker
// drops everything it holds (NatsManager.OnReconnect), because it may have missed messages
// while it was away. What the time still bounds is a message that was lost with nobody
// disconnected (a subscriber too far behind to receive it), and a cache-aside read that was
// in flight across the change and re-stored what it read before it (a read that missed,
// went to the database, and was overtaken by a commit and its eviction before it wrote
// back: a window of milliseconds that the time to live then keeps open for as long as the
// time is).
//
// Which writes evict which cache is the inventory the tests pin, write path by write path:
//
//   - DeviceByToken (tenant|token): UpdateDevice (Api.UpdateDevice, which a re-type goes
//     through), device delete (EvictEntityDelete). Read per event when the device token is
//     trusted (auth disabled, optional with no credential, a transport-authenticated
//     event). A credentialed event takes its device from Credentials instead. The
//     raise-alarm consumer, whose drop of an edge for a deleted device must hold at once,
//     reads devices through the plain Api instead (main.go, newRaiseAlarmConsumer).
//   - RelationshipsBySource (tenant|source device id): relationship create (single, bulk,
//     device claim), relationship remove (single, bulk, a claim reopened), an entity delete
//     (the deleted device's own set, and the set of every device tracking it), and an update
//     of a relationship type (its tracked flag decides which edges a set holds).
//   - ProfileResolutionByType (tenant|device type id): UpdateDeviceType, a profile publish
//     and a rollback (fanned out over the adopting types), and a geofence mutation that
//     minted a fence-set version (fanned out over the tenant's types). A device type that
//     devices reference cannot be deleted, so no entry outlives its type.
//   - MembershipsByEntity (tenant|family|id) and ScopedGroupsExist (tenant): every
//     membership mutation (attribute recompute, group register, deregister and delete,
//     entity delete).
//   - Credentials (hash of tenant, type and id): every write to a credential (update,
//     delete) or to its device (update, replacement, delete).
//
// A publish or rollback commits the version and its scope memberships together, but they sit
// in three caches whose copies are dropped by separate messages. For the moment between them
// another replica can stamp an event with the new version and the old memberships, or the
// reverse. The event is still stamped with ONE version (the resolution is one entry), and
// scope arming already could not rely on sub-TTL visibility.
//
// A tenant's erasure drops its entries from every cache here, on every replica: user-management
// clears the buckets and then broadcasts a tenant-wide eviction on each cache's subject
// (messaging.BroadcastTenantCacheEviction), so a deleted tenant's devices stop resolving from
// memory when the purge runs rather than when their copies expire.
//
// Credentials is the sixth in-process copy, and it has no bucket behind it. It holds at most
// CredentialCacheMaxEntries entries and cfg.InMemoryCache.CredentialCacheMiB.
//
// 🔑 THE THREE CACHES KEYED BY DEVICE ARE SIZED FOR THE FLEET; THE OTHER TWO ARE NOT.
// DeviceByToken, RelationshipsBySource and MembershipsByEntity hold an entry per device
// (MembershipsByEntity also one per area or asset a device is tracked to), so each keeps
// cfg.InMemoryCache's bound (131,072 entries and 24 MiB by default), where messaging's
// default of 4,096 entries evicted an entry for every event once a replica saw more than
// 4,096 devices within the time to live. ProfileResolutionByType and ScopedGroupsExist are keyed by
// device type and by tenant, of which there are few, and keep that default on purpose: a
// larger bound there would only be memory promised and never used. What the budget costs
// against the memory limit is set out at config.DefaultPerDeviceCacheMiB. The bound does
// not lengthen how long any entry is kept. The split is pinned
// through this function by TestPerDeviceCachesHoldMoreThanTheDefaultBound.
//
// A new cache whose readers need another replica's write visible at once is built with
// messaging.WithoutLocalCache(), with a comment here saying why, and is taken out of
// TestEveryDeviceManagementCacheKeepsItsInProcessTier, which otherwise fails on it: the
// decision above is pinned there, through this function, over a real broker. A new cache
// that keeps the tier must also be built WithCrossReplicaEviction, or its entries outlive a
// change on another replica by the whole time to live: that test pins it too.
func InitializeCaches(nmgr *messaging.NatsManager, cfg *config.DeviceManagementConfiguration) (*Caches, error) {
	inMemoryTtl := time.Duration(cfg.InMemoryCache.TtlSeconds) * time.Second
	common := []messaging.CacheOption{
		messaging.WithLocalTTL(inMemoryTtl),
		messaging.WithCrossReplicaEviction(),
	}
	perDevice := messaging.WithLocalBounds(cfg.InMemoryCache.PerDeviceCacheEntries,
		cfg.InMemoryCache.PerDeviceCacheMiB<<20)
	deviceByToken, err := nmgr.NewCache(CACHE_NAME_DEVICE_BY_TOKEN,
		time.Duration(cfg.DeviceCacheTtlSeconds)*time.Second,
		append(common, perDevice, deviceCharge.option())...)
	if err != nil {
		return nil, err
	}
	relationshipsBySource, err := nmgr.NewCache(CACHE_NAME_RELATIONSHIPS_BY_SOURCE,
		time.Duration(cfg.RelationshipCacheTtlSeconds)*time.Second,
		append(common, perDevice, relationshipsCharge.option())...)
	if err != nil {
		return nil, err
	}
	// TTL'd from the metric-definition knob, whose name predates the fold: the entry
	// still holds the metric definitions, and renaming an operator-visible key is not
	// worth the break.
	profileResolutionByType, err := nmgr.NewCache(CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE,
		time.Duration(cfg.MetricDefCacheTtlSeconds)*time.Second,
		append(common, resolutionCharge.option())...)
	if err != nil {
		return nil, err
	}
	membershipsByEntity, err := nmgr.NewCache(CACHE_NAME_MEMBERSHIPS_BY_ENTITY,
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second,
		append(common, perDevice, membershipsCharge.option())...)
	if err != nil {
		return nil, err
	}
	// The scoped-groups-exist gate shares the membership cache's invalidation cadence.
	scopedGroupsExist, err := nmgr.NewCache(CACHE_NAME_SCOPED_GROUPS_EXIST,
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second,
		append(common, scopedGroupsCharge.option())...)
	if err != nil {
		return nil, err
	}
	// The credential cache, and the broadcast that empties it on every replica. Subscribed
	// before the Api that fills it exists, so no credential is held on a replica that cannot
	// yet hear an eviction. The subscription ends with the connection, as the buckets'
	// handles do. A reconnect empties it: it may have missed evictions while away.
	bcast := nmgr.NewEvictionBroadcast(CredentialCacheName)
	credentials := NewCredentialCache(CredentialCacheMaxEntries, cfg.InMemoryCache.CredentialCacheMiB<<20,
		WithCredentialCacheTTL(inMemoryTtl),
		WithCredentialCacheMetrics(nmgr.Microservice), WithCredentialEvictionBroadcast(bcast))
	if err := bcast.Subscribe(credentials.ApplyEviction); err != nil {
		return nil, fmt.Errorf("subscribing to credential cache evictions: %w", err)
	}
	nmgr.OnReconnect(credentials.Clear)
	return &Caches{
		DeviceByToken:           deviceByToken,
		RelationshipsBySource:   relationshipsBySource,
		ProfileResolutionByType: profileResolutionByType,
		MembershipsByEntity:     membershipsByEntity,
		ScopedGroupsExist:       scopedGroupsExist,
		Credentials:             credentials,
	}, nil
}

// decodedCharge is what one cache here is charged, against its in-process byte bound, for
// the decoded copy it keeps beside the encoded bytes: the encoded length times factor, plus
// base. The factors are the measured ratio of heap to JSON length for what each cache
// holds, rounded up, and TestDecodedChargeCoversTheHeap measures it again and fails if a
// charge falls below the heap. A resolution is a slice of structs with pointer fields and
// holds several times its encoding; the others hold about what they encode, so they are not
// charged more, which would shrink the per-device caches' capacity for nothing.
type decodedCharge struct{ factor, base int }

var (
	deviceCharge        = decodedCharge{factor: 1, base: 64}
	relationshipsCharge = decodedCharge{factor: 2, base: 64}
	resolutionCharge    = decodedCharge{factor: 4, base: 64}
	membershipsCharge   = decodedCharge{factor: 1, base: 64}
	scopedGroupsCharge  = decodedCharge{factor: 1, base: 64}
)

func (c decodedCharge) option() messaging.CacheOption {
	return messaging.WithDecodedCharge(c.factor, c.base)
}

// bytes is what the cache charges for a decoded value of encoded length n.
func (c decodedCharge) bytes(n int) int { return n*c.factor + c.base }
