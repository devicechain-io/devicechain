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
	chans := make([]<-chan string, 500)
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
			require.Equal(t, "one:"+value, hubReceive(t, ch))
			require.Equal(t, "one:"+value, hubReceive(t, wild))
		}
	}
	require.Equal(t, int64(1500), decoded.Load())
	f.feed("two").ch <- Message{Key: []byte("0"), Value: []byte("isolated")}
	require.Equal(t, "two:isolated", hubReceive(t, other))
	for _, ch := range chans {
		select {
		case v := <-ch:
			t.Fatalf("unexpected %s", v)
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
	require.Equal(t, "one:fresh", hubReceive(t, ch))
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
		require.Equal(t, "one:alive", hubReceive(t, ch2))
		stop()
		hubClosed(t, ch2)
	}
}

func TestLiveHubSlowSubscriber(t *testing.T) {
	var f hubFake
	h := NewLiveHub("test", f.open, hubDecode, 0)
	var drops atomic.Uint64
	h.OnDrop(func() { drops.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	fast, err := h.Subscribe(ctx, "one", nil)
	require.NoError(t, err)
	for i := 0; i < 1000; i++ {
		value := fmt.Sprint(i)
		f.feed("one").ch <- Message{Value: []byte(value)}
		require.Equal(t, "one:"+value, hubReceive(t, fast))
	}
	require.Eventually(t, func() bool { return h.Dropped() == 1000-liveBuffer && drops.Load() == 1000-liveBuffer }, 5*time.Second, time.Millisecond)
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
	require.Equal(t, "one:reopened", hubReceive(t, fresh))
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
	chans := make([]<-chan string, 500)
	for i := range chans {
		chans[i], err = h.Subscribe(ctx, "acme", []string{fmt.Sprint(i)})
		require.NoError(t, err)
	}
	require.Equal(t, baseline+1, srv.NumSubscriptions())
	for round := 0; round < 2; round++ {
		for i, ch := range chans {
			value := fmt.Sprint(i)
			require.NoError(t, nc.Publish(ScopedSubject("test", "acme", streams.AlarmEvents), []byte(value)))
			require.Equal(t, value, hubReceive(t, ch))
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
	require.Equal(t, "fresh", hubReceive(t, ch))
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
		ch  <-chan string
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
	require.Equal(t, "one:still live", hubReceive(t, b.ch))
	cancel2()
	hubClosed(t, b.ch)
	hubCancelled(t, upctx)
}
