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
	return &Caches{
		DeviceByToken:         deviceByToken,
		RelationshipsBySource: relationshipsBySource,
	}, nil
}
