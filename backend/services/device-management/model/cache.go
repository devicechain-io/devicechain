// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

const (
	CACHE_NAME_DEVICE_BY_TOKEN         = "device-by-token"
	CACHE_NAME_RELATIONSHIPS_BY_SOURCE = "relationships-by-source"
)

// CacheSettings holds the name, size, and TTL used to construct a named cache.
type CacheSettings struct {
	Name string
	Size int
	TTL  time.Duration
}

// Caches bundles the caches the cached API decorator reads from and evicts. The
// hot inbound-event resolution path repeats two lookups for a small set of
// devices (ADR-022 review B2): device-token->device and a device's tracked
// relationships. Both are cached here; everything else falls through to the DB.
type Caches struct {
	// DeviceByToken caches positive device-by-token lookups (keyed by tenant+token).
	DeviceByToken *core.RedisCache
	// RelationshipsBySource caches a device's tracked relationships (keyed by
	// tenant+source device id).
	RelationshipsBySource *core.RedisCache
}

// Create a new cache for the given settings.
func newCacheForSettings(rdb *rdb.RdbManager, settings CacheSettings) *core.RedisCache {
	return rdb.NewRedisCache(settings.Name, settings.Size, settings.TTL)
}

// InitializeCaches builds the caches used by the cached API, sized and TTL'd from
// the service configuration (ADR-022 decision 1). The returned bundle is held by
// CachedApi so it can serve, populate, and evict entries.
func InitializeCaches(rdb *rdb.RdbManager, cfg *config.DeviceManagementConfiguration) *Caches {
	return &Caches{
		DeviceByToken: newCacheForSettings(rdb, CacheSettings{
			Name: CACHE_NAME_DEVICE_BY_TOKEN,
			Size: cfg.DeviceCacheSize,
			TTL:  time.Duration(cfg.DeviceCacheTtlSeconds) * time.Second,
		}),
		RelationshipsBySource: newCacheForSettings(rdb, CacheSettings{
			Name: CACHE_NAME_RELATIONSHIPS_BY_SOURCE,
			Size: cfg.RelationshipCacheSize,
			TTL:  time.Duration(cfg.RelationshipCacheTtlSeconds) * time.Second,
		}),
	}
}
