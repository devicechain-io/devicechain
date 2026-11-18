// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

type hubFeed struct {
	ch  chan Message
	ctx context.Context
}
type hubFake struct {
	mu    sync.Mutex
	feeds map[string][]hubFeed
	fail  bool
}

func (f *hubFake) open(ctx context.Context, tenant string) (<-chan Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.feeds == nil {
		f.feeds = make(map[string][]hubFeed)
	}
	ch := make(chan Message, 2048)
	f.feeds[tenant] = append(f.feeds[tenant], hubFeed{ch, ctx})
	if f.fail {
		f.fail = false
		return nil, errors.New("open failed")
	}
	return ch, nil
}
func (f *hubFake) feed(tenant string) hubFeed {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.feeds[tenant]
	return a[len(a)-1]
}
func (f *hubFake) count(tenant string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.feeds[tenant])
}
func hubReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		require.True(t, ok)
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}
func hubClosed[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case _, ok := <-ch:
		require.False(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed")
	}
}
func hubDecode(msg Message, tenant string) []Keyed[string] {
	return []Keyed[string]{{Key: string(msg.Key), Value: tenant + ":" + string(msg.Value)}}
}
func hubCancelled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not cancelled")
	}
}

func TestLiveHubFanout(t *testing.T) {
	var f hubFake
	var decoded atomic.Int64
	h := NewLiveHub("test", f.open, func(m Message, tenant string) []Keyed[string] { decoded.Add(1); return hubDecode(m, tenant) }, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	chans := make([]<-chan []string, 500)
	for i := range chans {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch, err := h.Subscribe(ctx, "one", []string{fmt.Sprint(i), fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
				return
			}
			chans[i] = ch
		}(i)
	}
	wg.Wait()
	require.Equal(t, 1, f.count("one"))
	wild, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	other, err := h.Subscribe(ctx, "two", []string{"0"})
	require.NoError(t, err)
	require.Equal(t, 1, f.count("two"))
	for round := 0; round < 3; round++ {
		for i, ch := range chans {
			key := fmt.Sprint(i)
			value := fmt.Sprintf("%d/%s", round, key)
			f.feed("one").ch <- Message{Key: []byte(key), Value: []byte(value)}
			require.Equal(t, []string{"one:" + value}, hubReceive(t, ch))
			require.Equal(t, []string{"one:" + value}, hubReceive(t, wild))
		}
	}
	require.Equal(t, int64(1500), decoded.Load())
	f.feed("two").ch <- Message{Key: []byte("0"), Value: []byte("isolated")}
	require.Equal(t, []string{"two:isolated"}, hubReceive(t, other))
	for _, ch := range chans {
		select {
		case v := <-ch:
			t.Fatalf("unexpected %v", v)
		default:
		}
	}
	cancel()
	for _, ch := range chans {
		hubClosed(t, ch)
	}
	hubClosed(t, wild)
	hubClosed(t, other)
	hubCancelled(t, f.feed("one").ctx)
	hubCancelled(t, f.feed("two").ctx)
	next, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	ch, err := h.Subscribe(next, "one", []string{"0"})
	require.NoError(t, err)
	require.Equal(t, 2, f.count("one"))
	f.feed("one").ch <- Message{Key: []byte("0"), Value: []byte("fresh")}
	require.Equal(t, []string{"one:fresh"}, hubReceive(t, ch))
}

func TestLiveHubLifecycleRace(t *testing.T) {
	var f hubFake
	h := NewLiveHub("test", f.open, hubDecode, 0)
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := h.Subscribe(ctx, "one", nil)
		require.NoError(t, err)
		done := make(chan struct{})
		go func() { cancel(); close(done) }()
		next, stop := context.WithCancel(context.Background())
		ch2, err := h.Subscribe(next, "one", nil)
		require.NoError(t, err)
		<-done
		hubClosed(t, ch)
		f.feed("one").ch <- Message{Value: []byte("alive")}
		require.Equal(t, []string{"one:alive"}, hubReceive(t, ch2))
		stop()
		hubClosed(t, ch2)
	}
}

func TestLiveHubMessageBatches(t *testing.T) {
	var f hubFake
	var decoded atomic.Int64
	h := NewLiveHub("test", f.open, func(msg Message, tenant string) []Keyed[int] {
		base := int(msg.Value[0]) * 64
		values := make([]Keyed[int], 64)
		for i := range values {
			values[i] = Keyed[int]{Key: fmt.Sprint(i % 2), Value: base + i}
		}
		decoded.Add(1)
		return values
	}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wild, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	keyed, err := h.Subscribe(ctx, "one", []string{"1", "1"})
	require.NoError(t, err)
	missing, err := h.Subscribe(ctx, "one", []string{"missing"})
	require.NoError(t, err)
	for i := 0; i < 100; i++ {
		f.feed("one").ch <- Message{Value: []byte{byte(i)}}
	}
	// Wait for dispatch to complete before either subscriber starts reading.
	require.Eventually(t, func() bool { return len(wild) == 100 && len(keyed) == 100 }, 5*time.Second, time.Millisecond)
	require.Equal(t, int64(100), decoded.Load())
	require.Zero(t, h.Dropped())
	for i := 0; i < 100; i++ {
		batch := hubReceive(t, wild)
		require.Len(t, batch, 64)
		for j, value := range batch {
			require.Equal(t, i*64+j, value)
		}
		// The keyed batch must have its own backing slice.
		for j := range batch {
			batch[j] = -1
		}
		matches := hubReceive(t, keyed)
		require.Len(t, matches, 32)
		for j, value := range matches {
			require.Equal(t, i*64+j*2+1, value)
		}
	}
	close(f.feed("one").ch)
	hubClosed(t, wild)
	hubClosed(t, keyed)
	hubClosed(t, missing)
	require.Zero(t, h.Dropped())
}

func TestLiveHubSlowSubscriber(t *testing.T) {
	var f hubFake
	h := NewLiveHub("test", f.open, func(msg Message, tenant string) []Keyed[string] {
		value := tenant + ":" + string(msg.Value)
		return []Keyed[string]{{Value: value}, {Value: value + "/second"}}
	}, 0)
	var drops atomic.Uint64
	h.OnDrop(func() { drops.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	fast, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	for i := 0; i < 1000; i++ {
		value := "one:" + fmt.Sprint(i)
		f.feed("one").ch <- Message{Value: []byte(fmt.Sprint(i))}
		require.Equal(t, []string{value, value + "/second"}, hubReceive(t, fast))
	}
	wantDrops := uint64((1000 - liveBuffer) * 2)
	require.Eventually(t, func() bool { return h.Dropped() == wantDrops && drops.Load() == wantDrops }, 5*time.Second, time.Millisecond)
	require.Len(t, slow, liveBuffer)
	for i := 0; i < liveBuffer; i++ {
		value := "one:" + fmt.Sprint(i)
		require.Equal(t, []string{value, value + "/second"}, hubReceive(t, slow))
	}
	close(f.feed("one").ch)
	hubClosed(t, slow)
	hubClosed(t, fast)
}

func TestLiveHubDeathLimitAndOpenFailure(t *testing.T) {
	var f hubFake
	f.fail = true
	h := NewLiveHub("test", f.open, hubDecode, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := h.Subscribe(ctx, "one", nil)
	require.Error(t, err)
	a, stop := context.WithCancel(ctx)
	ch, err := h.Subscribe(a, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 2, f.count("one"))
	b, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	c, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	_, err = h.Subscribe(ctx, "one", nil)
	var le *limit.Error
	require.ErrorAs(t, err, &le)
	require.Equal(t, 4, le.Got)
	require.Equal(t, 3, le.Max)
	require.Equal(t, limit.Code, le.Extensions()["code"])
	_, err = h.Subscribe(ctx, "two", nil)
	require.NoError(t, err)
	stop()
	hubClosed(t, ch)
	d, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	close(f.feed("one").ch)
	hubClosed(t, b)
	hubClosed(t, c)
	hubClosed(t, d)
	fresh, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 3, f.count("one"))
	f.feed("one").ch <- Message{Value: []byte("reopened")}
	require.Equal(t, []string{"one:reopened"}, hubReceive(t, fresh))
}

func TestLiveHubEmbeddedNATS(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	cfg := &config.InstanceConfiguration{}
	cfg.ApplyDefaults()
	nmgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "area", InstanceConfiguration: *cfg}, nc: nc}
	var decoded atomic.Int64
	h := NewLiveHub("alarms", func(ctx context.Context, tenant string) (<-chan Message, error) {
		return nmgr.SubscribeLive(ctx, tenant, streams.AlarmEvents)
	}, func(m Message, tenant string) []Keyed[string] {
		decoded.Add(1)
		return []Keyed[string]{{Key: string(m.Value), Value: string(m.Value)}}
	}, 0)
	baseline := srv.NumSubscriptions()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chans := make([]<-chan []string, 500)
	for i := range chans {
		chans[i], err = h.Subscribe(ctx, "acme", []string{fmt.Sprint(i)})
		require.NoError(t, err)
	}
	require.Equal(t, baseline+1, srv.NumSubscriptions())
	for round := 0; round < 2; round++ {
		for i, ch := range chans {
			value := fmt.Sprint(i)
			require.NoError(t, nc.Publish(ScopedSubject("test", "acme", streams.AlarmEvents), []byte(value)))
			require.Equal(t, []string{value}, hubReceive(t, ch))
		}
	}
	require.Equal(t, int64(1000), decoded.Load())
	cancel()
	for _, ch := range chans {
		hubClosed(t, ch)
	}
	require.Eventually(t, func() bool { return srv.NumSubscriptions() == baseline }, 5*time.Second, time.Millisecond)
	next, stop := context.WithCancel(context.Background())
	defer stop()
	ch, err := h.Subscribe(next, "acme", []string{"fresh"})
	require.NoError(t, err)
	require.Equal(t, baseline+1, srv.NumSubscriptions())
	require.NoError(t, nc.Publish(ScopedSubject("test", "acme", streams.AlarmEvents), []byte("fresh")))
	require.Equal(t, []string{"fresh"}, hubReceive(t, ch))
	stop()
	hubClosed(t, ch)
	require.Eventually(t, func() bool { return srv.NumSubscriptions() == baseline }, 5*time.Second, time.Millisecond)
}

// Neither first nor concurrent subscribers return before open confirms the feed.
// The first caller owns only its subscription, not the shared upstream context.
func TestLiveHubOpenOwnership(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := make(chan Message)
	var upctx context.Context
	var opens atomic.Int64
	h := NewLiveHub("test", func(ctx context.Context, tenant string) (<-chan Message, error) {
		opens.Add(1)
		upctx = ctx
		close(started)
		<-release
		return upstream, nil
	}, hubDecode, 0)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	type result struct {
		ch  <-chan []string
		err error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() { ch, err := h.Subscribe(ctx1, "one", nil); first <- result{ch, err} }()
	<-started
	go func() { ch, err := h.Subscribe(ctx2, "one", nil); second <- result{ch, err} }()
	select {
	case <-first:
		t.Fatal("returned before open completed")
	case <-second:
		t.Fatal("joined unconfirmed feed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	a, b := hubReceive(t, first), hubReceive(t, second)
	require.NoError(t, a.err)
	require.NoError(t, b.err)
	cancel1()
	hubClosed(t, a.ch)
	require.NoError(t, upctx.Err())
	require.Equal(t, int64(1), opens.Load())
	upstream <- Message{Value: []byte("still live")}
	require.Equal(t, []string{"one:still live"}, hubReceive(t, b.ch))
	cancel2()
	hubClosed(t, b.ch)
	hubCancelled(t, upctx)
}

// pinnedOpen records each open under the binding the hub pinned on its context,
// refusing an open that carries no pin for the tenant it was asked to open.
func (f *hubFake) pinnedOpen(ctx context.Context, tenant string) (<-chan Message, error) {
	pin, ok := ctx.Value(livePinKey{}).(livePin)
	if !ok || pin.tenant != tenant {
		return nil, fmt.Errorf("open for %q carries no pin", tenant)
	}
	return f.open(ctx, pin.live)
}

func TestSharedLiveHubPlacementMove(t *testing.T) {
	m, _ := metricsManager(t, startEmbeddedServer(t))
	var group atomic.Int32
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(func(string) (int, error) {
		return int(group.Load()), nil
	}), n1Config(streams.ResolvedEvents)))
	h := SharedLiveHub(m, streams.ResolvedEvents, func() *LiveHub[string] {
		return NewLiveHub("test", func(ctx context.Context, tenant string) (<-chan Message, error) {
			return m.SubscribeLive(ctx, tenant, streams.ResolvedEvents)
		}, hubDecode, 3)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := h.Subscribe(ctx, "tenant-a", nil)
	require.NoError(t, err)
	before, err := m.tenantSuffix("tenant-a", streams.ResolvedEvents)
	require.NoError(t, err)
	group.Store(1)
	after, err := m.tenantSuffix("tenant-a", streams.ResolvedEvents)
	require.NoError(t, err)
	require.NotEqual(t, before, after, "a live manager must observe placement changes")
	b, err := h.Subscribe(ctx, "tenant-a", nil)
	require.NoError(t, err)
	require.NoError(t, m.nc.Publish(ScopedSubject("test", "tenant-a", after), []byte("group-b")))
	require.Equal(t, []string{"tenant-a:group-b"}, hubReceive(t, b))
	// Existing subscribers retain their original feed until they leave.
	require.NoError(t, m.nc.Publish(ScopedSubject("test", "tenant-a", before), []byte("group-a")))
	require.Equal(t, []string{"tenant-a:group-a"}, hubReceive(t, a))
	group.Store(-1)
	_, directErr := m.SubscribeLive(ctx, "tenant-a", streams.ResolvedEvents)
	_, sharedErr := h.Subscribe(ctx, "tenant-a", nil)
	require.ErrorIs(t, directErr, ErrUnplacedTenant)
	require.ErrorIs(t, sharedErr, ErrUnplacedTenant)
}

// SubscribeLive listens on the group a hub pinned, not on a placement that
// moved after the hub compared it; a pin for another tenant is ignored.
func TestSubscribeLiveHonoursPin(t *testing.T) {
	m, _ := metricsManager(t, startEmbeddedServer(t))
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(func(string) (int, error) {
		return 0, nil
	}), n1Config(streams.ResolvedEvents)))
	group1 := GroupSuffix(streams.ResolvedEvents, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pin := func(tenant string) context.Context {
		return context.WithValue(ctx, livePinKey{}, livePin{tenant: tenant, suffix: streams.ResolvedEvents, live: group1})
	}
	pinned, err := m.SubscribeLive(pin("tenant-a"), "tenant-a", streams.ResolvedEvents)
	require.NoError(t, err)
	other, err := m.SubscribeLive(pin("tenant-b"), "tenant-a", streams.ResolvedEvents)
	require.NoError(t, err)
	require.NoError(t, m.nc.Publish(ScopedSubject("test", "tenant-a", group1), []byte("pinned")))
	require.NoError(t, m.nc.Publish(ScopedSubject("test", "tenant-a", GroupSuffix(streams.ResolvedEvents, 0)), []byte("resolved")))
	require.Equal(t, "pinned", string(hubReceive(t, pinned).Value))
	require.Equal(t, "resolved", string(hubReceive(t, other).Value))
}

func TestLiveHubConcurrentBindingOpenLimit(t *testing.T) {
	var f hubFake
	var group atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	h := NewLiveHub("test", func(ctx context.Context, tenant string) (<-chan Message, error) {
		if pin, _ := ctx.Value(livePinKey{}).(livePin); pin.live == "0" {
			close(started)
			<-release
		}
		return f.pinnedOpen(ctx, tenant)
	}, hubDecode, 1)
	h.binding = func(string) (string, error) { return fmt.Sprint(group.Load()), nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := h.Subscribe(ctx, "one", nil); result <- err }()
	<-started
	group.Store(1)
	b, err := h.Subscribe(ctx, "one", nil)
	// Always release the blocked opener, including on an assertion failure.
	close(release)
	require.NoError(t, err)
	var le *limit.Error
	require.ErrorAs(t, hubReceive(t, result), &le)
	require.Equal(t, 2, le.Got)
	require.Equal(t, 1, le.Max)
	hubCancelled(t, f.feed("0").ctx)
	f.feed("1").ch <- Message{Value: []byte("winner")}
	require.Equal(t, []string{"one:winner"}, hubReceive(t, b))
}

// The hub keeps the opener build supplied (the services' test seams replace it)
// and hands it the tenant, with the resolved group pinned on the context.
func TestSharedLiveHubBindingOpens(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(fmt.Sprint("move=", move), func(t *testing.T) {
			m := &NatsManager{Microservice: &core.Microservice{InstanceId: "test"}}
			var group atomic.Int32
			require.NoError(t, m.EnableStreamSharding(shardRouterFunc(func(string) (int, error) {
				return int(group.Load()), nil
			}), n1Config(streams.ResolvedEvents)))
			var f hubFake
			h := SharedLiveHub(m, streams.ResolvedEvents, func() *LiveHub[string] {
				return NewLiveHub("test", f.pinnedOpen, hubDecode, 3)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a, err := h.Subscribe(ctx, "tenant-a", nil)
			require.NoError(t, err)
			if move {
				group.Store(1)
			}
			b, err := h.Subscribe(ctx, "tenant-a", nil)
			require.NoError(t, err)
			require.Equal(t, 1, f.count(GroupSuffix(streams.ResolvedEvents, 0)))
			if move {
				require.Equal(t, 1, f.count(GroupSuffix(streams.ResolvedEvents, 1)), "placement move must open a second binding")
			} else {
				require.Zero(t, f.count(GroupSuffix(streams.ResolvedEvents, 1)))
			}
			f.feed(GroupSuffix(streams.ResolvedEvents, int(group.Load()))).ch <- Message{Value: []byte("current")}
			require.Equal(t, []string{"tenant-a:current"}, hubReceive(t, b))
			if !move {
				require.Equal(t, []string{"tenant-a:current"}, hubReceive(t, a))
			}
		})
	}
}

func TestLiveHubBindingLimitAndRefusal(t *testing.T) {
	var f hubFake
	h := NewLiveHub("test", f.pinnedOpen, hubDecode, 2)
	binding := "A"
	var resolveErr error
	h.binding = func(string) (string, error) { return binding, resolveErr }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old, stop := context.WithCancel(ctx)
	a, err := h.Subscribe(old, "one", nil)
	require.NoError(t, err)
	binding = "B"
	b, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.count("A"))
	require.Equal(t, 1, f.count("B"))
	f.feed("B").ch <- Message{Value: []byte("new group")}
	require.Equal(t, []string{"one:new group"}, hubReceive(t, b))
	resolveErr = ErrUnplacedTenant
	_, err = h.Subscribe(ctx, "one", nil)
	require.ErrorIs(t, err, ErrUnplacedTenant)
	resolveErr = nil
	binding = "C"
	_, err = h.Subscribe(ctx, "one", nil)
	var le *limit.Error
	require.ErrorAs(t, err, &le)
	require.Equal(t, 3, le.Got)
	require.Equal(t, 2, le.Max)
	require.Zero(t, f.count("C"), "rebinding must not bypass the tenant-wide ceiling")
	stop()
	hubClosed(t, a)
	hubCancelled(t, f.feed("A").ctx)
	binding = "B"
	c, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.count("B"), "old feed teardown must not remove the current feed")
	f.feed("B").ch <- Message{Value: []byte("shared")}
	require.Equal(t, []string{"one:shared"}, hubReceive(t, b))
	require.Equal(t, []string{"one:shared"}, hubReceive(t, c))
	close(f.feed("B").ch)
	hubClosed(t, b)
	hubClosed(t, c)
}

func TestLiveHubPinsBindingWithoutHoldingLocks(t *testing.T) {
	var f hubFake
	h := NewLiveHub("test", f.pinnedOpen, hubDecode, 2)
	binding := "A"
	h.binding = func(string) (string, error) { return binding, nil }
	h.open = func(ctx context.Context, tenant string) (<-chan Message, error) {
		require.True(t, h.mu.TryLock(), "directory lock held across open")
		lt := h.tenants["one"]
		h.mu.Unlock()
		require.True(t, lt.mu.TryLock(), "tenant lock held across open")
		lt.mu.Unlock()
		// Placement can change while the broker confirms the resolved subject.
		binding = "B"
		return f.pinnedOpen(ctx, tenant)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	b, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.count("A"))
	require.Equal(t, 1, f.count("B"))
	f.feed("A").ch <- Message{Value: []byte("A")}
	f.feed("B").ch <- Message{Value: []byte("B")}
	require.Equal(t, []string{"one:A"}, hubReceive(t, a))
	require.Equal(t, []string{"one:B"}, hubReceive(t, b))
}

// With shard groups off, on a bare manager as the services' tests use, the
// binding never changes: one open, the build's opener called with the tenant.
func TestSharedLiveHubDisabledSharesBinding(t *testing.T) {
	m := &NatsManager{}
	var f hubFake
	h := SharedLiveHub(m, streams.ResolvedEvents, func() *LiveHub[string] {
		return NewLiveHub("test", f.open, hubDecode, 2)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	b, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	require.Equal(t, 1, f.count("one"))
	live, pinned := pinnedLiveSuffix(f.feed("one").ctx, "one", streams.ResolvedEvents)
	require.True(t, pinned)
	require.Equal(t, streams.ResolvedEvents, live)
	f.feed("one").ch <- Message{Value: []byte("unchanged")}
	require.Equal(t, []string{"one:unchanged"}, hubReceive(t, a))
	require.Equal(t, []string{"one:unchanged"}, hubReceive(t, b))
}
