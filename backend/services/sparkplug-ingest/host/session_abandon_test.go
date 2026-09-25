// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive onConnected and runLoop through fakes, for the parts a real broker cannot
// make deterministic: whether the failover probe ran, and which backoff runLoop chose.
// subscribe_refusal_broker_test.go covers the same rule against a broker that really
// refuses.

// refuseFilters is a subscribe seam that refuses the named filters the way
// SubscribeMqttConfirmed reports a SUBACK 0x80, grants the rest, and records every
// filter it was asked for.
type refuseFilters struct {
	mu      sync.Mutex
	refused map[string]bool
	asked   []string
}

func (r *refuseFilters) subscribe(_ mqtt.Client, filter string, _ byte, _ mqtt.MessageHandler, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, filter)
	if r.refused[filter] {
		return messaging.ErrSubscriptionRefused
	}
	return nil
}

func (r *refuseFilters) askedFor() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// abandonClient is a client over three groups with a reconciler that asserts one live
// device, a tiny probe window and a recording rebirth publisher: everything the
// failover probe needs to run and to be seen running.
func abandonClient(t *testing.T, fake *fakeIngester, seam *refuseFilters) (*Client, prometheus.Counter, *atomic.Int32) {
	t.Helper()
	const bigSession = uint64(5_000_000_000_000_000_000)
	failures := prometheus.NewCounter(prometheus.CounterOpts{Name: "subscribe_failures_total"})
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1", "g2", "g3"}},
		Broker{}, fake, fixedNow, Metrics{SubscribeFailures: failures})
	c.SetReconciler(&fakeReconciler{devices: []AssertedDevice{{ExternalId: "g1/node", SessionId: bigSession}}, max: bigSession})
	c.probeWindow = 5 * time.Millisecond
	c.subscribe = seam.subscribe
	rebirths := new(atomic.Int32)
	c.rebirthPub = func(_, _ string) bool { rebirths.Add(1); return true }
	return c, failures, rebirths
}

func lostSignalled(lost chan struct{}) bool {
	select {
	case <-lost:
		return true
	default:
		return false
	}
}

// A refused group ends the session: no ONLINE, no failover probe, the failure counted
// once, the remaining groups not even asked for, and runLoop told to disconnect.
//
// The probe is the half a broker test cannot see. Run without ONLINE, its rebirths are
// answered by nothing, so after the window it declares the asserted device DISCONNECTED
// although nothing is known to be wrong with it.
func TestARefusedGroupAnnouncesNoOnlineAndRunsNoProbe(t *testing.T) {
	fake := &fakeIngester{}
	seam := &refuseFilters{refused: map[string]bool{Namespace + "/g2/#": true}}
	c, failures, rebirths := abandonClient(t, fake, seam)

	fc := newFakeClient()
	lost := make(chan struct{}, 1)
	up := new(atomic.Bool)
	c.onConnected(context.Background(), fc, 1000, lost, up)

	assert.NotContains(t, fc.pubs, c.stateTopic, "ONLINE was announced although a group was refused")
	assert.Equal(t, float64(1), testutil.ToFloat64(failures), "the refused group was not counted exactly once")
	assert.Equal(t, []string{Namespace + "/g1/#", Namespace + "/g2/#"}, seam.askedFor(),
		"subscribing carried on past the refusal on a session that is being abandoned")
	assert.True(t, lostSignalled(lost), "runLoop was not told to end the session")
	assert.False(t, up.Load(), "an abandoned session was marked fully up, which resets the backoff")

	// Give a probe (had one been launched) several windows to act.
	time.Sleep(20 * c.probeWindow)
	c.probeWG.Wait()
	assert.Equal(t, int32(0), rebirths.Load(), "the failover probe rebirthed nodes on a session that never went ONLINE")
	assert.Equal(t, 0, fake.presenceCount(), "the failover probe declared devices DISCONNECTED on a session that never went ONLINE")
}

// The counterweight, on the same client: with every group granted the session is
// announced, marked up, and the probe runs and does its job (a silent asserted device is
// declared DISCONNECTED). Without it, a client that never probed at all would satisfy
// the test above.
func TestAllGroupsGrantedStillAnnouncesOnlineAndProbes(t *testing.T) {
	fake := &fakeIngester{}
	seam := &refuseFilters{}
	c, failures, rebirths := abandonClient(t, fake, seam)

	fc := newFakeClient()
	lost := make(chan struct{}, 1)
	up := new(atomic.Bool)
	c.onConnected(context.Background(), fc, 1000, lost, up)

	online, err := ParseState(fc.pubs[c.stateTopic])
	require.NoError(t, err)
	assert.True(t, online.Online)
	assert.Len(t, seam.askedFor(), 3)
	assert.Equal(t, float64(0), testutil.ToFloat64(failures))
	assert.False(t, lostSignalled(lost))
	assert.True(t, up.Load(), "a fully established session was not marked up")

	c.probeWG.Wait()
	assert.Equal(t, int32(1), rebirths.Load(), "the probe did not rebirth the asserted node")
	require.Equal(t, 1, fake.presenceCount(), "the probe did not declare the silent asserted device")
	assert.Equal(t, "g1/node", fake.presence[0].ExternalId)
	assert.False(t, fake.presence[0].Connected)
}

// An ONLINE announcement the broker never acknowledged takes the same path as a refused
// group: the edge nodes may never have seen ONLINE, so they will not answer the probe's
// rebirths and probing would declare live devices dead.
func TestAFailedOnlineAnnouncementEndsTheSessionWithoutProbing(t *testing.T) {
	fake := &fakeIngester{}
	c, failures, rebirths := abandonClient(t, fake, &refuseFilters{})

	fc := newFakeClient()
	fc.pubErr = errors.New("publish not acknowledged")
	lost := make(chan struct{}, 1)
	up := new(atomic.Bool)
	c.onConnected(context.Background(), fc, 1000, lost, up)

	assert.True(t, lostSignalled(lost), "runLoop was not told to end a session whose ONLINE failed")
	assert.False(t, up.Load())
	assert.Equal(t, float64(0), testutil.ToFloat64(failures), "a publish failure is not a subscribe failure")
	time.Sleep(20 * c.probeWindow)
	c.probeWG.Wait()
	assert.Equal(t, int32(0), rebirths.Load(), "the probe ran although ONLINE was never acknowledged")
	assert.Equal(t, 0, fake.presenceCount())
}

// A connect handler that finishes after runLoop has ended its session must not install
// its client as the live connection again: that would point the rebirth publisher (and
// Stop's OFFLINE) back at a connection runLoop already disconnected.
func TestALateHandlerDoesNotReinstateAnEndedSession(t *testing.T) {
	c, _, _ := abandonClient(t, &fakeIngester{}, &refuseFilters{})
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	c.onConnected(ended, newFakeClient(), 1000, make(chan struct{}, 1), new(atomic.Bool))

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Nil(t, c.mc, "a handler for an ended session installed its client as the live connection")
}

// sessionScript is a fake connection factory for runLoop. Session i behaves as
// script[i] says; past the end of the script the loop's context is cancelled.
type sessionScript struct {
	cancel  context.CancelFunc
	script  []string // "refused" or "full-then-lost"
	mu      sync.Mutex
	clients []*fakeClient
}

func (s *sessionScript) newClient(opts *mqtt.ClientOptions) mqtt.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := len(s.clients)
	fc := newFakeClient()
	s.clients = append(s.clients, fc)
	if i >= len(s.script) {
		s.cancel()
		return &scriptedClient{fakeClient: fc}
	}
	return &scriptedClient{fakeClient: fc, opts: opts, lose: s.script[i] == "full-then-lost"}
}

// scriptedClient behaves like paho across Connect: it succeeds and then runs the
// OnConnect handler on a goroutine of its own. A "full-then-lost" session then drops
// the connection once the handler has finished.
type scriptedClient struct {
	*fakeClient
	opts *mqtt.ClientOptions
	lose bool
}

func (s *scriptedClient) Connect() mqtt.Token {
	if s.opts != nil {
		go func() {
			s.opts.OnConnect(s)
			if s.lose {
				s.opts.OnConnectionLost(s, errors.New("connection reset"))
			}
		}()
	}
	return fakeToken{}
}

// The backoff is reset only by a FULL session. Three abandoned sessions back off 1s, 2s,
// 4s like failed connects; a full session that is later lost reconnects at once and
// resets the backoff, so the next abandoned session starts again at 1s. Every abandoned
// connection is disconnected.
func TestAnAbandonedSessionBacksOffAndAFullSessionResets(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1"}},
		Broker{}, nil, fixedNow, Metrics{})
	// The session's outcome is decided by the script, through the subscribe seam: a
	// "refused" session refuses g1.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := &sessionScript{cancel: cancel,
		script: []string{"refused", "refused", "refused", "full-then-lost", "refused"}}
	c.newClient = script.newClient
	c.subscribe = func(client mqtt.Client, _ string, _ byte, _ mqtt.MessageHandler, _ time.Duration) error {
		if client.(*scriptedClient).lose {
			return nil
		}
		return messaging.ErrSubscriptionRefused
	}
	var mu sync.Mutex
	var slept []time.Duration
	c.backoffSleep = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return true
	}

	c.wg.Add(1)
	done := make(chan struct{})
	go func() { c.runLoop(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runLoop did not finish the scripted sessions")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, time.Second}, slept,
		"abandoned sessions must back off like failed connects, and only a full session resets it")
	script.mu.Lock()
	defer script.mu.Unlock()
	for i := range script.script {
		assert.Equal(t, int32(1), script.clients[i].disconnects.Load(),
			"session %d (%s) was not disconnected when it ended", i, script.script[i])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Nil(t, c.mc, "the ended session is still recorded as the live connection")
}

// runScriptedSessions drives runLoop through one session per entry of script (then
// cancels) and returns each session's fake connection. prepare runs on each fake before
// its session connects.
func runScriptedSessions(t *testing.T, c *Client, script []string, prepare func(i int, fc *fakeClient)) []*fakeClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sessionScript{cancel: cancel, script: script}
	c.newClient = func(opts *mqtt.ClientOptions) mqtt.Client {
		cl := s.newClient(opts)
		s.mu.Lock()
		i := len(s.clients) - 1
		fc := s.clients[i]
		s.mu.Unlock()
		if i < len(script) {
			prepare(i, fc)
		}
		return cl
	}
	c.backoffSleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	c.wg.Add(1)
	done := make(chan struct{})
	go func() { c.runLoop(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runLoop did not finish the scripted sessions")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fakeClient(nil), s.clients...)
}

// An ONLINE the client gave up waiting for may still have been stored by the broker as
// the retained STATE, and the clean disconnect that ends the abandoned session discards
// the Last-Will that would have replaced it. So the abandoned session publishes its own
// retained OFFLINE, stamped with its session timestamp, BEFORE disconnecting; otherwise
// the stale ONLINE stands, and edge nodes flush into a host that is not subscribed.
//
// The fake records a publish even when it fails it, which is the case that matters: the
// broker kept the message and only the acknowledgement went missing.
func TestAnAbandonedSessionOverwritesAPossiblyRetainedOnlineBeforeDisconnecting(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1"}},
		Broker{}, nil, fixedNow, Metrics{})
	c.subscribe = grantAll

	clients := runScriptedSessions(t, c, []string{"online-unacked"}, func(_ int, fc *fakeClient) {
		fc.pubErr = errors.New("PUBACK not received in time")
	})

	fc := clients[0]
	states := fc.sentTo(c.stateTopic)
	require.Len(t, states, 2, "the abandoned session did not publish OFFLINE after its unacknowledged ONLINE")
	online, err := ParseState(states[0].payload)
	require.NoError(t, err)
	assert.True(t, online.Online)
	offline, err := ParseState(states[1].payload)
	require.NoError(t, err)
	assert.False(t, offline.Online, "the second STATE of an abandoned session must be OFFLINE")
	assert.True(t, states[1].retained, "an OFFLINE that is not retained does not replace a retained ONLINE")
	assert.Equal(t, online.Timestamp, offline.Timestamp, "the OFFLINE must carry the session's own timestamp")

	events := fc.eventLog()
	require.GreaterOrEqual(t, len(events), 2)
	assert.Equal(t, "disconnect", events[len(events)-1], "the OFFLINE must be published before the clean disconnect")
	assert.Equal(t, "pub:"+c.stateTopic, events[len(events)-2], "the OFFLINE must be published before the clean disconnect")
}

// The counterweight: a FULL session that the broker later drops publishes no OFFLINE of
// its own on the way out, because the broker publishes its Last-Will. Only its ONLINE is
// ever sent on that connection.
func TestALostFullSessionLeavesItsOfflineToTheWill(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"g1"}},
		Broker{}, nil, fixedNow, Metrics{})
	c.subscribe = grantAll

	clients := runScriptedSessions(t, c, []string{"full-then-lost"}, func(int, *fakeClient) {})

	states := clients[0].sentTo(c.stateTopic)
	require.Len(t, states, 1, "a full session that was lost published a STATE besides its ONLINE")
	online, err := ParseState(states[0].payload)
	require.NoError(t, err)
	assert.True(t, online.Online)
}
