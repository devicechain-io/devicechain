// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package cmdreceiver

import (
	"encoding/json"
	"sync"
	"testing"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publishingClient is the fake client with a Publish that records what a device answered.
type publishingClient struct {
	*fakeClient
	mu        sync.Mutex
	published [][]byte
	topics    []string
}

func (c *publishingClient) Publish(topic string, _ byte, _ bool, payload interface{}) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.topics = append(c.topics, topic)
	c.published = append(c.published, payload.([]byte))
	return &fakeToken{completes: true}
}

func (c *publishingClient) answers() []responseEnvelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]responseEnvelope, len(c.published))
	for i, p := range c.published {
		_ = json.Unmarshal(p, &out[i])
	}
	return out
}

// fakeMessage is the one paho message field the handler path reads.
type fakeMessage struct{ payload []byte }

func (m fakeMessage) Duplicate() bool   { return false }
func (m fakeMessage) Qos() byte         { return 1 }
func (m fakeMessage) Retained() bool    { return false }
func (m fakeMessage) Topic() string     { return "" }
func (m fakeMessage) MessageID() uint16 { return 0 }
func (m fakeMessage) Payload() []byte   { return m.payload }
func (m fakeMessage) Ack()              {}

// recordingHandler keeps what it was handed and decides, per test, whether to answer.
type recordingHandler struct {
	mu       sync.Mutex
	received []Command
	devices  []string
	answers  []func(bool)
}

func (h *recordingHandler) Receive(device string, c Command, answer func(bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.devices = append(h.devices, device)
	h.received = append(h.received, c)
	h.answers = append(h.answers, answer)
}

func deviceWithClient(t *testing.T, r *Receiver, token string) (*deviceState, *publishingClient) {
	t.Helper()
	ds := r.newTestDevice(token)
	c := &publishingClient{fakeClient: newFakeClient()}
	ds.client = c
	return ds, c
}

const boostFrame = `{"token":"cmd-1","deviceToken":"probe-1","name":"boost-cooling",` +
	`"payload":{"minutes":15},"dispatchNonce":"n-1"}`

// WITHOUT a handler the receiver is what it always was: a frame is answered SUCCESSFUL at once.
// Every harness that uses the receiver depends on this, so it is pinned beside the new path.
func TestWithoutAHandlerAFrameIsAnsweredAtOnce(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	ds, c := deviceWithClient(t, r, "probe-1")

	r.onMessage(ds)(nil, fakeMessage{[]byte(boostFrame)})

	answers := c.answers()
	require.Len(t, answers, 1)
	assert.True(t, answers[0].Success)
	assert.Equal(t, "cmd-1", answers[0].CommandToken)
	assert.Equal(t, "n-1", answers[0].DispatchNonce)
}

// WITH a handler the receiver answers NOTHING on receipt: the handler owns when, and with what.
// This is the whole point of the hook: a command that takes time to take effect must not be
// reported SUCCESSFUL the instant it arrives.
func TestAHandlerOwnsWhenACommandIsAnswered(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	h := &recordingHandler{}
	r.SetHandler(h)
	ds, c := deviceWithClient(t, r, "probe-1")

	r.onMessage(ds)(nil, fakeMessage{[]byte(boostFrame)})

	require.Len(t, h.received, 1)
	assert.Equal(t, "probe-1", h.devices[0])
	assert.Equal(t, "cmd-1", h.received[0].Token)
	assert.Equal(t, "boost-cooling", h.received[0].Name)
	assert.JSONEq(t, `{"minutes":15}`, string(h.received[0].Payload))
	assert.Empty(t, c.answers(), "the receiver answered a command its handler had not settled")

	// The handler settles it later, FAILED this time: the answer carries the outcome AND the
	// nonce the frame named, and goes to the device's own response topic.
	h.answers[0](false)
	answers := c.answers()
	require.Len(t, answers, 1)
	assert.False(t, answers[0].Success, "a failed command must be answered as failed, not as a success")
	assert.Equal(t, "cmd-1", answers[0].CommandToken)
	assert.Equal(t, "n-1", answers[0].DispatchNonce)
	assert.Equal(t, r.responseTopic("probe-1"), c.topics[0])
}

// A frame addressed to another device never reaches the handler, exactly as it is never answered.
func TestAMisroutedFrameNeverReachesTheHandler(t *testing.T) {
	r := New("i1", "t1", "ssl://broker", nil)
	h := &recordingHandler{}
	r.SetHandler(h)
	ds, c := deviceWithClient(t, r, "probe-1")

	r.onMessage(ds)(nil, fakeMessage{[]byte(`{"token":"cmd-9","deviceToken":"probe-2","name":"restart"}`)})

	assert.Empty(t, h.received)
	assert.Empty(t, c.answers())
}

// The outcome payload carries the verdict and nothing else, and the success form is unchanged.
func TestOutcomePayload(t *testing.T) {
	for _, ok := range []bool{true, false} {
		raw, err := outcomePayload("c", "n", ok)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, map[string]any{"commandToken": "c", "success": ok, "dispatchNonce": "n"}, got)
	}
	a, _ := responsePayload("c", "n")
	b, _ := outcomePayload("c", "n", true)
	assert.Equal(t, string(b), string(a))
}
