// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging_test

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/test/msgtest"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func TestManagerFlushCacheFollowUpsRemovesLateFill(t *testing.T) {
	ctx := context.Background()
	kv := msgtest.NewMemoryKV()
	c, flush, pending := messaging.ShutdownCacheForTest(t, kv)
	require.NoError(t, c.Set(ctx, "k", "old"))
	require.NoError(t, c.Delete(ctx, "k"))
	require.True(t, pending(), "the test must leave a follow-up armed")
	_, err := kv.Put(ctx, messaging.KVKeyForTest("k"), []byte(`"late"`))
	require.NoError(t, err)
	flush(ctx)
	_, err = kv.Get(ctx, messaging.KVKeyForTest("k"))
	require.ErrorIs(t, err, jetstream.ErrKeyNotFound, "shutdown left the late fill in the bucket")
}

func TestManagerFlushCacheFollowUpsClosesScheduling(t *testing.T) {
	ctx := context.Background()
	c, flush, pending := messaging.ShutdownCacheForTest(t, msgtest.NewMemoryKV())
	require.NoError(t, c.Delete(ctx, "before"))
	flush(ctx)
	require.False(t, pending(), "shutdown left a follow-up armed")
	require.NoError(t, c.Delete(ctx, "k"))
	require.False(t, pending(), "Delete armed a follow-up after shutdown")
}
