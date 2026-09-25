// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeToken is an always-succeeded paho token: mqtt.WaitTokenTimeout reads Done()
// then Error(), so a closed Done channel + nil Error is an immediate success.
type fakeToken struct{}

func (fakeToken) Wait() bool                     { return true }
func (fakeToken) WaitTimeout(time.Duration) bool { return true }
func (fakeToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (fakeToken) Error() error                   { return nil }

// failedToken is a completed paho token that carries an error, the shape a publish the
// broker never acknowledged comes back as.
type failedToken struct{ err error }

func (failedToken) Wait() bool                     { return true }
func (failedToken) WaitTimeout(time.Duration) bool { return true }
func (failedToken) Done() <-chan struct{}          { ch := make(chan struct{}); close(ch); return ch }
func (t failedToken) Error() error                 { return t.err }

// fakeClient is a minimal mqtt.Client that records the ordered sequence of
// subscribe/publish calls so a test can assert both what was published and that
// the subscribe happened before the STATE birth.
type fakeClient struct {
	mu     sync.Mutex
	events []string // "sub:<topic>" / "pub:<topic>" in call order
	pubs   map[string][]byte
	// pubErr, when set, fails every Publish with it.
	pubErr error
	// disconnects counts Disconnect calls.
	disconnects atomic.Int32
}

func newFakeClient() *fakeClient { return &fakeClient{pubs: map[string][]byte{}} }

func (f *fakeClient) record(ev string) { f.mu.Lock(); f.events = append(f.events, ev); f.mu.Unlock() }

func (f *fakeClient) Subscribe(topic string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	f.record("sub:" + topic)
	return fakeToken{}
}

func (f *fakeClient) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	f.mu.Lock()
	f.pubs[topic] = payload.([]byte)
	f.mu.Unlock()
	f.record("pub:" + topic)
	if f.pubErr != nil {
		return failedToken{f.pubErr}
	}
	return fakeToken{}
}

func (f *fakeClient) IsConnected() bool      { return true }
func (f *fakeClient) IsConnectionOpen() bool { return true }
func (f *fakeClient) Connect() mqtt.Token    { return fakeToken{} }
func (f *fakeClient) Disconnect(uint)        { f.disconnects.Add(1) }
func (f *fakeClient) SubscribeMultiple(map[string]byte, mqtt.MessageHandler) mqtt.Token {
	return fakeToken{}
}
func (f *fakeClient) Unsubscribe(...string) mqtt.Token        { return fakeToken{} }
func (f *fakeClient) AddRoute(string, mqtt.MessageHandler)    {}
func (f *fakeClient) OptionsReader() mqtt.ClientOptionsReader { return mqtt.ClientOptionsReader{} }

// TestStateTimestampIsFreshPerSession is the B4 check-that-cannot-fail (and the
// replacement for the earlier test that enshrined a fixed lifetime timestamp).
// Each MQTT session must stamp its OFFLINE Last-Will and its ONLINE birth with
// the SAME, FRESH timestamp: matched within a session so an edge node can pair
// them, and different ACROSS sessions so a delayed OFFLINE from a prior session
// (older timestamp) is rejected rather than mistaken for the live host's death.
// If a change froze the timestamp for the client's lifetime, the two sessions'
// ONLINE stamps would collide and this test goes red.
func TestStateTimestampIsFreshPerSession(t *testing.T) {
	c := NewClient(config.SparkplugSource{Tenant: "t", HostId: "h"}, Broker{}, nil, nil, Metrics{})
	// Every group granted. The fake's Subscribe returns a plain token, which the real
	// confirmed subscribe (rightly) reads as a refusal, and a refused group now ends the
	// session before ONLINE; this test's subject is the timestamps and their order, so it
	// grants through the seam and records the call the way the client makes it.
	c.subscribe = grantAll

	// Session A at timestamp 1000: the will (from the options) and the ONLINE
	// birth (published by onConnected) must both carry 1000.
	willA, err := ParseState(c.sessionOptions(context.Background(), 1000, make(chan struct{}, 1), new(atomic.Bool)).WillPayload)
	require.NoError(t, err)
	assert.False(t, willA.Online)
	assert.Equal(t, int64(1000), willA.Timestamp)

	fcA := newFakeClient()
	c.onConnected(context.Background(), fcA, 1000, make(chan struct{}, 1), new(atomic.Bool))
	onlineA, err := ParseState(fcA.pubs[c.stateTopic])
	require.NoError(t, err)
	assert.True(t, onlineA.Online)
	assert.Equal(t, willA.Timestamp, onlineA.Timestamp, "ONLINE and will must match within a session")

	// Subscribe-before-birth ordering (Sparkplug session order): the STATE publish
	// must come after every subscribe, or an edge node could flush buffered data
	// before this clean-session client has a live subscription.
	assert.Equal(t, []string{"sub:spBv1.0/#", "pub:" + c.stateTopic}, fcA.events)

	// Session B at a later timestamp 2000: fresh, and again internally matched.
	willB, err := ParseState(c.sessionOptions(context.Background(), 2000, make(chan struct{}, 1), new(atomic.Bool)).WillPayload)
	require.NoError(t, err)
	assert.Equal(t, int64(2000), willB.Timestamp)

	fcB := newFakeClient()
	c.onConnected(context.Background(), fcB, 2000, make(chan struct{}, 1), new(atomic.Bool))
	onlineB, err := ParseState(fcB.pubs[c.stateTopic])
	require.NoError(t, err)
	assert.Equal(t, willB.Timestamp, onlineB.Timestamp)

	assert.NotEqual(t, onlineA.Timestamp, onlineB.Timestamp, "each session must get a FRESH timestamp")
}

// grantAll is a subscribe seam that grants every filter, going through the client's own
// Subscribe so a fakeClient still records the call in order.
func grantAll(client mqtt.Client, filter string, qos byte, cb mqtt.MessageHandler, _ time.Duration) error {
	client.Subscribe(filter, qos, cb) //subconfirm:ok a test seam standing in for the confirmed subscribe; the grant is this fake's answer
	return nil
}
