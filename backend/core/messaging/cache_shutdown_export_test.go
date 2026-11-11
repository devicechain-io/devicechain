// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"
)

// ShutdownCacheForTest exposes only the shutdown path to tests using msgtest.MemoryKV,
// which itself imports messaging and therefore cannot be used by in-package tests.
func ShutdownCacheForTest(t *testing.T, store cacheStore) (*Cache, func(context.Context), func() bool) {
	t.Helper()
	c := NewCacheOver(store, WithCrossReplicaEviction())
	c.followUpDelay = time.Hour
	b := newEvictionBroadcast(evictConn(t, evictRig(t)), crossReplicaSubject, "test", nil)
	if err := c.subscribeEvictions(b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.fuMu.Lock()
		defer c.fuMu.Unlock()
		if c.fuTimer != nil {
			c.fuTimer.Stop()
		}
	})
	nmgr := &NatsManager{}
	nmgr.registerCache(c)
	return c, nmgr.flushCacheFollowUps, func() bool {
		c.fuMu.Lock()
		defer c.fuMu.Unlock()
		return c.fuTimer != nil || len(c.fuPending) != 0
	}
}

func KVKeyForTest(key string) string { return kvKey(key) }
