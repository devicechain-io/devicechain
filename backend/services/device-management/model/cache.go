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
	// process memory for CredentialCacheTTL, in front of AuthenticateDevice. Nil means
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
// 🔑 EVERY ONE OF THEM KEEPS ITS IN-PROCESS TIER, AND THAT WAS DECIDED CACHE BY CACHE. A
// messaging.Cache holds what it read in process memory for up to 5 s (never longer than
// the bucket TTL below), so a change made through ANOTHER replica reaches this one's events
// up to 5 s later than through the bucket alone. Each of these was already cache-aside
// with TTL-bounded staleness (a read racing a mutation can re-store the old value until
// the TTL, see MembershipsForEntity), so what changes is the size of a bound every reader
// already lives with:
//
//   - DeviceByToken: read per event when the device token is trusted (auth disabled,
//     optional with no credential, a transport-authenticated event). A device deleted, or
//     deleted and re-created under the same token, through another replica resolves to its
//     old row for up to 5 s here. A credentialed event takes its device from Credentials
//     instead, which a device delete through ANY replica empties for that device on every
//     replica by broadcast, so its events are normally refused on the next one, and within
//     5 s on a replica the broadcast did not reach. The raise-alarm consumer, whose drop of
//     an edge for a deleted device must hold at once, reads devices through the plain Api
//     instead (main.go, newRaiseAlarmConsumer).
//   - RelationshipsBySource: a new or removed tracked edge reaches events on other
//     replicas within 5 s.
//   - ProfileResolutionByType, MembershipsByEntity, ScopedGroupsExist: a publish or
//     rollback commits the version and its scope memberships together, but they sit in
//     three caches whose copies expire independently. For up to 5 s after one, another
//     replica can stamp an event with the new version and the old memberships, or the
//     reverse — so a rule whose group scope changed can be evaluated against the previous
//     scope. The event is still stamped with ONE version (the resolution is one entry),
//     and scope arming already could not rely on sub-TTL visibility.
//
// Credentials is the sixth in-process copy, and it has no bucket behind it. It holds a
// credential for up to 5 s after the database read that verified it began, and every write
// that changes a credential or its device evicts it on every replica (see CredentialCache),
// so the 5 s is what a LOST eviction costs. It holds at most CredentialCacheMaxEntries
// entries and CredentialCacheMaxBytes, whatever cfg.InMemoryCache says.
//
// None of them is on the tenant-erasure path. The erasure fence is a database write
// callback and reads no cache. The KV purge runs in user-management against the buckets
// directly. A replica can still write a purged tenant's key back for a few seconds — an
// entry served from memory sends the resolver on to the next lookup, whose miss is filled
// from the database into the bucket — but the purge sweeps the buckets on every pass, a
// pass that removed anything restarts the settle window, and that window is held above
// messaging.RetainedCacheWindow plus the purge timeout, far longer than 5 s.
//
// A tenant purge evicts nothing from Credentials, and needs to evict nothing: a deleting
// tenant's connects and ingest are refused well before any of its rows is purged, and every
// entry dies 5 s after its read began, so no entry read from live rows survives into the
// purge. What remains is events already queued for that tenant when the purge deletes its
// credentials: for up to 5 s they can authenticate from memory where the database would
// refuse them.
//
// 🔑 THE THREE CACHES KEYED BY DEVICE ARE SIZED FOR THE FLEET; THE OTHER TWO ARE NOT.
// DeviceByToken, RelationshipsBySource and MembershipsByEntity hold an entry per device
// (MembershipsByEntity also one per area or asset a device is tracked to), so each keeps
// cfg.InMemoryCache's bound (131,072 entries and 24 MiB by default), where messaging's
// default of 4,096 entries evicted an entry for every event once a replica saw more than
// 4,096 devices within 5 s. ProfileResolutionByType and ScopedGroupsExist are keyed by
// device type and by tenant, of which there are few, and keep that default on purpose: a
// larger bound there would only be memory promised and never used. What the budget costs
// against the memory limit is set out at config.DefaultPerDeviceCacheMiB. The bound does
// not lengthen how long any entry is kept (that is still the 5 s above, capped at the
// bucket TTL), so the erasure argument above is unchanged by it. The split is pinned
// through this function by TestPerDeviceCachesHoldMoreThanTheDefaultBound.
//
// A new cache whose readers need another replica's write visible at once is built with
// messaging.WithoutLocalCache(), with a comment here saying why, and is taken out of
// TestEveryDeviceManagementCacheKeepsItsInProcessTier, which otherwise fails on it: the
// decision above is pinned there, through this function, over a real broker.
func InitializeCaches(nmgr *messaging.NatsManager, cfg *config.DeviceManagementConfiguration) (*Caches, error) {
	perDevice := messaging.WithLocalBounds(cfg.InMemoryCache.PerDeviceCacheEntries,
		cfg.InMemoryCache.PerDeviceCacheMiB<<20)
	deviceByToken, err := nmgr.NewCache(CACHE_NAME_DEVICE_BY_TOKEN,
		time.Duration(cfg.DeviceCacheTtlSeconds)*time.Second, perDevice)
	if err != nil {
		return nil, err
	}
	relationshipsBySource, err := nmgr.NewCache(CACHE_NAME_RELATIONSHIPS_BY_SOURCE,
		time.Duration(cfg.RelationshipCacheTtlSeconds)*time.Second, perDevice)
	if err != nil {
		return nil, err
	}
	// TTL'd from the metric-definition knob, whose name predates the fold: the entry
	// still holds the metric definitions, and renaming an operator-visible key is not
	// worth the break.
	profileResolutionByType, err := nmgr.NewCache(CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE,
		time.Duration(cfg.MetricDefCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	membershipsByEntity, err := nmgr.NewCache(CACHE_NAME_MEMBERSHIPS_BY_ENTITY,
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second, perDevice)
	if err != nil {
		return nil, err
	}
	// The scoped-groups-exist gate shares the membership cache's invalidation cadence.
	scopedGroupsExist, err := nmgr.NewCache(CACHE_NAME_SCOPED_GROUPS_EXIST,
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	// The credential cache, and the broadcast that empties it on every replica. Subscribed
	// before the Api that fills it exists, so no credential is held on a replica that cannot
	// yet hear an eviction. The subscription ends with the connection, as the buckets'
	// handles do.
	bcast := nmgr.NewEvictionBroadcast(CredentialCacheName)
	credentials := NewCredentialCache(CredentialCacheMaxEntries, CredentialCacheMaxBytes,
		WithCredentialCacheMetrics(nmgr.Microservice), WithCredentialEvictionBroadcast(bcast))
	if err := bcast.Subscribe(credentials.ApplyEviction); err != nil {
		return nil, fmt.Errorf("subscribing to credential cache evictions: %w", err)
	}
	return &Caches{
		DeviceByToken:           deviceByToken,
		RelationshipsBySource:   relationshipsBySource,
		ProfileResolutionByType: profileResolutionByType,
		MembershipsByEntity:     membershipsByEntity,
		ScopedGroupsExist:       scopedGroupsExist,
		Credentials:             credentials,
	}, nil
}
