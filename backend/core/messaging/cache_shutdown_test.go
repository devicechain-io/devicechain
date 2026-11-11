// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// shutdownReplica is a cross-replica Cache over store whose follow-up is an hour away, so only
// the stop can run it, registered with a manager the way NewCache registers it.
func shutdownReplica(t *testing.T, store cacheStore) (*Cache, *NatsManager) {
	t.Helper()
	c := replicaOver(t, evictRig(t), store)
	c.followUpDelay = time.Hour
	t.Cleanup(func() {
		c.fuMu.Lock()
		defer c.fuMu.Unlock()
		if c.fuTimer != nil {
			c.fuTimer.Stop()
		}
	})
	nmgr := &NatsManager{}
	nmgr.registerCache(c)
	return c, nmgr
}

func followUpPending(c *Cache) bool {
	c.fuMu.Lock()
	defer c.fuMu.Unlock()
	return c.fuTimer != nil || len(c.fuPending) != 0
}

// A replica that evicted a key less than the follow-up delay before stopping still deletes it
// a second time, so another replica's late fill does not survive to the time to live.
func TestManagerFlushCacheFollowUpsRemovesLateFill(t *testing.T) {
	ctx := context.Background()
	store := newCountingStore()
	c, nmgr := shutdownReplica(t, store)
	require.NoError(t, c.Set(ctx, "acme|k", "old"))
	require.NoError(t, c.Delete(ctx, "acme|k"))
	require.True(t, followUpPending(c), "the test must leave a follow-up armed")
	_, err := store.Put(ctx, kvKey("acme|k"), []byte(`"late"`)) // the other replica's late fill
	require.NoError(t, err)

	nmgr.flushCacheFollowUps(ctx)
	require.False(t, stored(store, "acme|k"), "shutdown left the late fill in the bucket")
}

func TestManagerFlushCacheFollowUpsClosesScheduling(t *testing.T) {
	ctx := context.Background()
	c, nmgr := shutdownReplica(t, newCountingStore())
	require.NoError(t, c.Delete(ctx, "acme|before"))
	nmgr.flushCacheFollowUps(ctx)
	require.False(t, followUpPending(c), "shutdown left a follow-up armed")
	require.NoError(t, c.Delete(ctx, "acme|k"))
	require.False(t, followUpPending(c), "Delete armed a follow-up after shutdown")
}
