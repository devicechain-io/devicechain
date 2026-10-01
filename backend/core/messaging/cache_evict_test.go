// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

const evictTestSubject = "$DC.inst-1.cache-evict.device-management.device-credentials"

// evictRig starts a plain broker (core NATS, no JetStream) and returns its URL.
func evictRig(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "the test broker never became ready")
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func evictConn(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// evictionRecorder collects what a subscription applied.
type evictionRecorder struct {
	mu  sync.Mutex
	got []CacheEviction
}

func (r *evictionRecorder) apply(e CacheEviction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
}

func (r *evictionRecorder) snapshot() []CacheEviction {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CacheEviction(nil), r.got...)
}

// waitFor polls until n evictions were applied, or fails.
func (r *evictionRecorder) waitFor(t *testing.T, n int) []CacheEviction {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.snapshot(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := r.snapshot()
	t.Fatalf("applied %d evictions within 5s, want %d: %+v", len(got), n, got)
	return nil
}

// Every replica applies what any of them publishes, the publisher included (core NATS echoes
// to the connection's own subscriptions), and each applies exactly the payload sent, once.
func TestAnEvictionReachesEverySubscriberIncludingThePublisher(t *testing.T) {
	url := evictRig(t)
	ma, _ := cacheMetricsFor(t)
	mb, _ := cacheMetricsFor(t)
	a := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", ma)
	b := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", mb)
	var ra, rb evictionRecorder
	require.NoError(t, a.Subscribe(ra.apply))
	require.NoError(t, b.Subscribe(rb.apply))

	require.NoError(t, a.Publish("acme", []string{"1", "2"}))

	want := CacheEviction{Tenant: "acme", Keys: []string{"1", "2"}}
	for name, r := range map[string]*evictionRecorder{"publisher": &ra, "other replica": &rb} {
		got := r.waitFor(t, 1)
		require.Equal(t, []CacheEviction{want}, got, "%s applied", name)
	}
	// Nothing further arrives: one publish is one application on each side.
	time.Sleep(50 * time.Millisecond)
	require.Len(t, ra.snapshot(), 1)
	require.Len(t, rb.snapshot(), 1)

	require.Equal(t, 1.0, testutil.ToFloat64(ma.cacheEvictBroadcasts.WithLabelValues("device-credentials", "published")))
	require.Equal(t, 0.0, testutil.ToFloat64(mb.cacheEvictBroadcasts.WithLabelValues("device-credentials", "published")))
	for name, m := range map[string]*streamMetrics{"publisher": ma, "other replica": mb} {
		require.Equal(t, 1.0, testutil.ToFloat64(m.cacheEvictBroadcasts.WithLabelValues("device-credentials", "received")),
			"%s received", name)
	}
}

// A list longer than one message allows arrives as several, whose keys concatenate back to
// the list in order.
func TestALongEvictionIsSplit(t *testing.T) {
	url := evictRig(t)
	m, _ := cacheMetricsFor(t)
	b := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", m)
	var r evictionRecorder
	require.NoError(t, b.Subscribe(r.apply))

	keys := make([]string, 1100)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	require.NoError(t, b.Publish("acme", keys))

	got := r.waitFor(t, 3)
	var sizes []int
	var all []string
	for _, e := range got {
		require.Equal(t, "acme", e.Tenant)
		sizes = append(sizes, len(e.Keys))
		all = append(all, e.Keys...)
	}
	require.Equal(t, []int{512, 512, 76}, sizes)
	require.Equal(t, keys, all)
	require.Equal(t, 3.0, testutil.ToFloat64(m.cacheEvictBroadcasts.WithLabelValues("device-credentials", "published")))
}

// A message that does not decode, names no tenant, carries no keys or carries too many is
// never applied, and each is counted. apply being called 0 times AND the counter reading 4
// is what tells a dropped message from one that never arrived.
func TestAMalformedEvictionIsDroppedAndCounted(t *testing.T) {
	url := evictRig(t)
	m, _ := cacheMetricsFor(t)
	b := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", m)
	var r evictionRecorder
	require.NoError(t, b.Subscribe(r.apply))

	tooMany := make([]string, MaxCacheEvictionKeys+1)
	for i := range tooMany {
		tooMany[i] = "1"
	}
	raw := evictConn(t, url)
	for _, body := range []string{
		"not json",
		`{"tenant":"","keys":["1"]}`,
		`{"tenant":"acme","keys":[]}`,
		`{"tenant":"acme","keys":["` + strings.Join(tooMany, `","`) + `"]}`,
	} {
		require.NoError(t, raw.Publish(evictTestSubject, []byte(body)))
	}
	// A well-formed one behind them: once it is applied, the four before it have been seen.
	require.NoError(t, raw.Publish(evictTestSubject, []byte(`{"tenant":"acme","keys":["7"]}`)))

	got := r.waitFor(t, 1)
	require.Equal(t, []CacheEviction{{Tenant: "acme", Keys: []string{"7"}}}, got)
	require.Equal(t, 4.0, testutil.ToFloat64(m.cacheEvictBroadcasts.WithLabelValues("device-credentials", "malformed")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheEvictBroadcasts.WithLabelValues("device-credentials", "received")))
}

// An eviction naming no tenant could only ever evict nothing, so it is refused loudly and
// nothing is sent.
func TestAnEvictionWithoutATenantIsRefused(t *testing.T) {
	url := evictRig(t)
	m, _ := cacheMetricsFor(t)
	b := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", m)
	var r evictionRecorder
	require.NoError(t, b.Subscribe(r.apply))

	require.Error(t, b.Publish("", []string{"1"}))
	require.NoError(t, b.Publish("acme", nil), "no keys is a no-op, not an error")
	require.NoError(t, b.Publish("acme", []string{"9"}))

	got := r.waitFor(t, 1)
	require.Equal(t, []CacheEviction{{Tenant: "acme", Keys: []string{"9"}}}, got)
	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheEvictBroadcasts.WithLabelValues("device-credentials", "published")))
}

// A second Subscribe would apply every eviction twice, and a Close after it would leave the
// first one running with no handle: it is refused.
func TestAnEvictionBroadcastSubscribesOnce(t *testing.T) {
	url := evictRig(t)
	b := newEvictionBroadcast(evictConn(t, url), evictTestSubject, "device-credentials", nil)
	var r evictionRecorder
	require.NoError(t, b.Close(), "closing a broadcast that never subscribed")
	require.NoError(t, b.Subscribe(r.apply))
	require.Error(t, b.Subscribe(r.apply))
	require.NoError(t, b.Close())
	require.NoError(t, b.Close())
}

// The eviction subject is a control subject: no stream captures it, so no stream can store
// an eviction and replay it later as though it were a platform message.
func TestTheEvictionSubjectIsOutsideEveryStream(t *testing.T) {
	subject := CacheEvictSubject("inst-1", "device-management", "device-credentials")
	require.Equal(t, evictTestSubject, subject)
	require.True(t, strings.HasPrefix(subject, "$DC.inst-1."))
	for _, s := range streams.All {
		for _, pattern := range StreamSubjects("inst-1", s.Suffix) {
			require.False(t, subjectMatches(pattern, subject),
				"stream %s captures the eviction subject %s through %s", s.Suffix, subject, pattern)
		}
	}
	// A name that is not a single subject token is made one, so it cannot widen the subject.
	require.Equal(t, "$DC.inst-1.cache-evict.device_management.a_b", CacheEvictSubject("inst-1", "device.management", "a.b"))
}

// The control-subject root moved into one constant shared with the cache evictions. A live
// subject must not move with it.
func TestTheDetectPurgeSubjectIsUnchanged(t *testing.T) {
	require.Equal(t, "$DC.i1.detect.tenant-purge", DetectPurgeSubject("i1"))
}
