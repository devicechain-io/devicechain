// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

const (
	CACHE_NAME_DEVICE_TYPE_BY_ID    = "device-type-by-id"
	CACHE_NAME_DEVICE_TYPE_BY_TOKEN = "device-type-by-token"
)

// Cache for device types by unique id.
var CACHE_DEVICE_TYPE_BY_ID = CacheSettings{
	Name: CACHE_NAME_DEVICE_TYPE_BY_ID,
	Size: 1000,
	TTL:  time.Minute,
}

// Cache for device types by unique id.
var CACHE_DEVICE_TYPE_BY_TOKEN = CacheSettings{
	Name: CACHE_NAME_DEVICE_TYPE_BY_TOKEN,
	Size: 1000,
	TTL:  time.Minute,
}

// Cache settings info.
type CacheSettings struct {
	Name string
	Size int
	TTL  time.Duration
}

// Create a new cache for the given settings.
func newCacheForSettings(rdb *rdb.RdbManager, settings CacheSettings) *core.RedisCache {
	return rdb.NewRedisCache(settings.Name, settings.Size, settings.TTL)
}

// Initialize caches for rdb objects.
func InitializeCaches(rdb *rdb.RdbManager) {
	newCacheForSettings(rdb, CACHE_DEVICE_TYPE_BY_ID)
	newCacheForSettings(rdb, CACHE_DEVICE_TYPE_BY_TOKEN)
}
