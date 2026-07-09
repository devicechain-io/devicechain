// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

const (
	CACHE_NAME_DEVICE_BY_TOKEN         = "device-by-token"
	CACHE_NAME_RELATIONSHIPS_BY_SOURCE = "relationships-by-source"
	CACHE_NAME_METRIC_DEFS_BY_TYPE     = "metric-defs-by-type"
	CACHE_NAME_PROFILE_SCOPE_BY_TYPE   = "profile-scope-by-type"
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
	return &Caches{
		DeviceByToken:         deviceByToken,
		RelationshipsBySource: relationshipsBySource,
		MetricDefsByType:      metricDefsByType,
		ProfileScopeByType:    profileScopeByType,
	}, nil
}
