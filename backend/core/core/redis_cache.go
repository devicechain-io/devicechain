/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package core

import (
	"context"
	"fmt"
	"time"

	cache "github.com/go-redis/cache/v8"
)

type RedisCache struct {
	Manager RedisManager
	Cache   *cache.Cache

	name   string
	prefix string
}

// Create a new cache with the given settings.
func NewRedisCache(manager RedisManager, name string, size int, ttl time.Duration) *RedisCache {
	// Create cache with options passed.
	rcache := cache.New(&cache.Options{
		Redis:      manager.Client,
		LocalCache: cache.NewTinyLFU(size, ttl),
	})

	wrapper := &RedisCache{
		Manager: manager,
		Cache:   rcache,
	}
	wrapper.name = name
	wrapper.prefix = fmt.Sprintf("%s_%s_%s_", manager.Microservice.InstanceId, manager.Microservice.FunctionalArea, name)
	return wrapper
}

// Set an entry in the cache.
func (rc *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	err := rc.Cache.Set(&cache.Item{
		Ctx:   ctx,
		Key:   rc.prefix + key,
		Value: value,
		TTL:   ttl,
	})
	if err != nil {
		return err
	}
	return nil
}

// Get an entry from the cache.
func (rc *RedisCache) Get(ctx context.Context, key string, callback func(*cache.Cache, string)) {
	callback(rc.Cache, rc.prefix+key)
}
