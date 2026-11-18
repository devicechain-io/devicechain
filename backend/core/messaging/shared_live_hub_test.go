// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSharedLiveHubRegistry(t *testing.T) {
	nmgr := &NatsManager{}
	var builds atomic.Int64
	build := func() *LiveHub[int] {
		builds.Add(1)
		return NewLiveHub[int]("test", nil, nil, DefaultMaxLiveSubscribersPerTenant)
	}
	var hubs [50]*LiveHub[int]
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range hubs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			hubs[i] = SharedLiveHub(nmgr, "test", build)
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int64(1), builds.Load())
	for _, hub := range hubs {
		require.Same(t, hubs[0], hub)
	}
	require.NotSame(t, hubs[0], SharedLiveHub(&NatsManager{}, "test", build))
	require.NotSame(t, hubs[0], SharedLiveHub(nmgr, "other", build))
	require.Equal(t, int64(3), builds.Load())
	require.PanicsWithValue(t, `messaging.SharedLiveHub: key "test" reused with a different value type`, func() {
		SharedLiveHub(nmgr, "test", func() *LiveHub[string] {
			t.Fatal("mismatched constructor must not run")
			return nil
		})
	})
}
