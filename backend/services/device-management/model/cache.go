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
	CACHE_NAME_DEVICE_BY_TOKEN         = kv.BucketDeviceByToken
	CACHE_NAME_RELATIONSHIPS_BY_SOURCE = kv.BucketRelationshipsBySource
	CACHE_NAME_METRIC_DEFS_BY_TYPE     = kv.BucketMetricDefsByType
	CACHE_NAME_PROFILE_SCOPE_BY_TYPE   = kv.BucketProfileScopeByType
	CACHE_NAME_MEMBERSHIPS_BY_ENTITY   = kv.BucketMembershipsByEntity
	CACHE_NAME_SCOPED_GROUPS_EXIST     = kv.BucketScopedGroupsExist
)

// Caches bundles the caches the cached API decorator reads from and evicts. The
// hot inbound-event resolution path repeats two lookups for a small set of
// devices (ADR-022 review B2): device-token->device and a device's tracked
// relationships. Both are cached here; everything else falls through to the DB.
type Caches struct {
	// DeviceByToken caches positive device-by-token lookups (keyed by tenant+token).
	DeviceByToken *messaging.Cache
	// RelationshipsBySource caches a device's tracked relationships (keyed by
	// tenant+source device id).
	RelationshipsBySource *messaging.Cache
	// MetricDefsByType caches the metric definitions resolved for a device type (via
	// its profile, ADR-045; keyed by tenant+device type id — what the hot path holds),
	// read on the hot path by ingest-time metric validation
	// (ADR-016). Empty results are cached too — an untyped device type is the common
	// case and should not query on every event.
	MetricDefsByType *messaging.Cache
	// ProfileScopeByType caches the device-type + active-published-profile-version
	// tokens denormalized onto every resolved event (ADR-051), keyed by tenant+device
	// type id. It shares MetricDefsByType's invalidation triggers — a type's profile
	// pointer changing (UpdateDeviceType) or the profile being published/rolled back
	// (evictProfileResolution) — PLUS one the metric-def cache lacks: a profile-TOKEN
	// rename, since the version token embeds the profile token (UpdateDeviceProfile).
	// Empty scopes (untyped/unpublished) are cached too so a device with no rules does
	// not query on every event.
	ProfileScopeByType *messaging.Cache
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
	metricDefsByType, err := nmgr.NewCache(CACHE_NAME_METRIC_DEFS_BY_TYPE,
		time.Duration(cfg.MetricDefCacheTtlSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	// The scope cache shares the metric-def cache's invalidation profile (ADR-051),
	// so it is TTL'd from the same configuration knob.
	profileScopeByType, err := nmgr.NewCache(CACHE_NAME_PROFILE_SCOPE_BY_TYPE,
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
		DeviceByToken:         deviceByToken,
		RelationshipsBySource: relationshipsBySource,
		MetricDefsByType:      metricDefsByType,
		ProfileScopeByType:    profileScopeByType,
		MembershipsByEntity:   membershipsByEntity,
		ScopedGroupsExist:     scopedGroupsExist,
	}, nil
}
