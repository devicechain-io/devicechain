// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
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
}

// InitializeCaches builds the caches used by the cached API, TTL'd from the
// service configuration (ADR-022 decision 1) and backed by NATS JetStream KV
// (ADR-007: NATS KV cache backend). The returned bundle is held by CachedApi so
// it can serve, populate, and evict entries.
func InitializeCaches(nmgr *messaging.NatsManager, cfg *config.DeviceManagementConfiguration) (*Caches, error) {
	deviceByToken, err := nmgr.NewCache(CACHE_NAME_DEVICE_BY_TOKEN,
		time.Duration(cfg.DeviceCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	relationshipsBySource, err := nmgr.NewCache(CACHE_NAME_RELATIONSHIPS_BY_SOURCE,
		time.Duration(cfg.RelationshipCacheTtlSeconds)*time.Second)
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
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	// The scoped-groups-exist gate shares the membership cache's invalidation cadence.
	scopedGroupsExist, err := nmgr.NewCache(CACHE_NAME_SCOPED_GROUPS_EXIST,
		time.Duration(cfg.MembershipCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return &Caches{
		DeviceByToken:           deviceByToken,
		RelationshipsBySource:   relationshipsBySource,
		ProfileResolutionByType: profileResolutionByType,
		MembershipsByEntity:     membershipsByEntity,
		ScopedGroupsExist:       scopedGroupsExist,
	}, nil
}
