// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const crossReplicaSubject = "$DC.inst-1.cache-evict.device-management.device-by-token"

// replicaOver builds one replica's Cache over the shared store, wired to the eviction
// broadcast on its own connection to the one broker, with the clock a test moves. The clock
// never advances in these tests: whatever a replica stops serving, it stops because it was
// told to, not because its copy expired.
func replicaOver(t *testing.T, url string, store cacheStore) *Cache {
	t.Helper()
	c, _ := localCacheOver(store)
	m, _ := cacheMetricsFor(t)
	b := newEvictionBroadcast(evictConn(t, url), crossReplicaSubject, "device-by-token", m)
	require.NoError(t, c.subscribeEvictions(b))
	t.Cleanup(func() { _ = b.Close() })
	return c
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("within 5s: %s", what)
}

// 🔑 THE POINT OF THE OPTION, BY VALUE: with the broadcast, another replica stops serving
// a deleted entry from memory at once, where TestAnotherReplicaSeesADeleteWithinTheLocalTTL
// shows it serving it until its copy expires. The fake clock never moves here.
func TestADeleteReachesAnotherReplicasMemoryWithoutWaitingForTheTTL(t *testing.T) {
	url := evictRig(t)
	store := newCountingStore()
	a := replicaOver(t, url, store)
	b := replicaOver(t, url, store)
	mustSet(t, a, "acme|dev", "v1")
	if v, _ := mustGet(t, b, "acme|dev"); v != "v1" {
		t.Fatalf("replica B read %q, want v1", v)
	}
	getsBefore := store.gets.Load()
	if v, _ := mustGet(t, b, "acme|dev"); v != "v1" || store.gets.Load() != getsBefore {
		t.Fatal("replica B did not answer its second read from memory, so the test proves nothing")
	}

	require.NoError(t, a.Delete(context.Background(), "acme|dev"))

	eventually(t, "replica B kept serving a deleted entry from memory", func() bool {
		_, found := mustGet(t, b, "acme|dev")
		return !found
	})
}

// Only the named key goes; the same tenant's other entries stay, and another tenant's key
// with the same suffix is untouched.
func TestABroadcastDeleteDropsOnlyItsKey(t *testing.T) {
	url := evictRig(t)
	store := newCountingStore()
	a := replicaOver(t, url, store)
	b := replicaOver(t, url, store)
	for _, k := range []string{"acme|1", "acme|2", "other|1"} {
		mustSet(t, a, k, "v")
		mustGet(t, b, k)
	}
	require.NoError(t, a.Delete(context.Background(), "acme|1"))
	eventually(t, "B dropped acme|1", func() bool { _, f := mustGet(t, b, "acme|1"); return !f })
	// The store lost acme|1 only, so a surviving answer proves B still holds its memory
	// copy of the others rather than having been flushed: re-seed the store to a new value
	// and B must still serve the old one.
	store.seed("acme|2", `"changed"`)
	store.seed("other|1", `"changed"`)
	if v, _ := mustGet(t, b, "acme|2"); v != "v" {
		t.Errorf("acme|2 on B = %q, want the value it held: a delete of another key flushed it", v)
	}
	if v, _ := mustGet(t, b, "other|1"); v != "v" {
		t.Errorf("other|1 on B = %q, want the value it held", v)
	}
}

// A tenant's erasure drops every entry of that tenant on every replica, and not the entries
// of a tenant whose name merely starts the same.
func TestATenantEvictionDropsEveryEntryOfThatTenantOnly(t *testing.T) {
	url := evictRig(t)
	store := newCountingStore()
	a := replicaOver(t, url, store)
	b := replicaOver(t, url, store)
	m, _ := cacheMetricsFor(t)
	pub := newEvictionBroadcast(evictConn(t, url), crossReplicaSubject, "device-by-token", m)

	for _, k := range []string{"acme|1", "acme|2", "acme", "acme-2|1"} {
		mustSet(t, a, k, "v")
		mustGet(t, b, k)
		store.seed(k, `"changed"`)
	}
	require.NoError(t, pub.PublishTenant("acme"))

	for _, k := range []string{"acme|1", "acme|2", "acme"} {
		k := k
		eventually(t, k+" dropped on B", func() bool { v, _ := mustGet(t, b, k); return v == "changed" })
	}
	if v, _ := mustGet(t, b, "acme-2|1"); v != "v" {
		t.Errorf("acme-2|1 on B = %q: evicting tenant acme must not reach tenant acme-2", v)
	}
}

// A replica that was away drops everything it held (NatsManager.OnReconnect registers this).
func TestClearDropsEverythingHeld(t *testing.T) {
	store := newCountingStore()
	c, _ := localCacheOver(store)
	mustSet(t, c, "acme|1", "v")
	mustSet(t, c, "beta|1", "v")
	store.seed("acme|1", `"changed"`)
	store.seed("beta|1", `"changed"`)
	c.local.clear()
	for _, k := range []string{"acme|1", "beta|1"} {
		if v, _ := mustGet(t, c, k); v != "changed" {
			t.Errorf("%s = %q after clear, want a fresh read of the bucket", k, v)
		}
	}
}

func TestTenantOfKey(t *testing.T) {
	for key, want := range map[string]string{
		"acme|12":          "acme",
		"acme|devices|3":   "acme",
		"acme":             "acme",
		"|orphan":          "",
		"acme-2|token|x|y": "acme-2",
	} {
		if got := tenantOfKey(key); got != want {
			t.Errorf("tenantOfKey(%q) = %q, want %q", key, got, want)
		}
	}
}

// A tenant eviction message with no keys is valid, and one with neither keys nor the flag
// is still dropped as malformed.
func TestATenantEvictionIsNotMalformedButAnEmptyOneIs(t *testing.T) {
	url := evictRig(t)
	m, _ := cacheMetricsFor(t)
	b := newEvictionBroadcast(evictConn(t, url), crossReplicaSubject, "device-by-token", m)
	var r evictionRecorder
	require.NoError(t, b.Subscribe(r.apply))
	pub := evictConn(t, url)
	require.NoError(t, pub.Publish(crossReplicaSubject, []byte(`{"tenant":"acme"}`)))
	require.NoError(t, b.PublishTenant("acme"))
	got := r.waitFor(t, 1)
	time.Sleep(50 * time.Millisecond)
	got = r.snapshot()
	require.Len(t, got, 1, "the message with neither keys nor all must be dropped")
	require.Equal(t, CacheEviction{Tenant: "acme", All: true}, got[0])
}

// A tenant's erasure reaches every device-management cache: one tenant-wide message on each
// bucket-backed cache's subject and on the credential cache's, naming the tenant and nothing
// else, and none for a cache that is retired.
func TestATenantErasureBroadcastsToEveryDeviceManagementCache(t *testing.T) {
	url := evictRig(t)
	m, _ := cacheMetricsFor(t)
	want := []string{
		DeviceCredentialCacheName, "device-by-token", "relationships-by-source",
		"memberships-by-entity", "profile-resolution-by-type", "scoped-groups-exist",
	}
	recorders := map[string]*evictionRecorder{}
	for _, name := range want {
		b := newEvictionBroadcast(evictConn(t, url), CacheEvictSubject("inst-1", "device-management", name), name, m)
		r := &evictionRecorder{}
		require.NoError(t, b.Subscribe(r.apply))
		recorders[name] = r
	}
	retired := newEvictionBroadcast(evictConn(t, url),
		CacheEvictSubject("inst-1", "device-management", retiredBucketMetricDefsByType), "retired", m)
	var rr evictionRecorder
	require.NoError(t, retired.Subscribe(rr.apply))

	require.NoError(t, BroadcastTenantCacheEviction(evictConn(t, url), "inst-1", "acme"))

	for name, r := range recorders {
		got := r.waitFor(t, 1)
		require.Equal(t, []CacheEviction{{Tenant: "acme", All: true}}, got, name)
	}
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, rr.snapshot(), "a retired cache must not be sent evictions")

	require.Error(t, BroadcastTenantCacheEviction(evictConn(t, url), "", "acme"))
	require.Error(t, BroadcastTenantCacheEviction(evictConn(t, url), "inst-1", ""))
}

// Everything registered with OnReconnect runs when the connection comes back, in order, and a
// Cache built through NewCache registers its in-process tier there. (Forcing a real
// reconnect needs a broker restart; the hook list is what the client's reconnect callback
// runs, and the Cache's clear is pinned by TestClearDropsEverythingHeld.)
func TestReconnectHooksRun(t *testing.T) {
	nmgr := &NatsManager{}
	var order []int
	nmgr.OnReconnect(func() { order = append(order, 1) })
	nmgr.OnReconnect(func() { order = append(order, 2) })
	nmgr.runReconnectHooks()
	require.Equal(t, []int{1, 2}, order)
	nmgr.runReconnectHooks()
	require.Equal(t, []int{1, 2, 1, 2}, order)
}
