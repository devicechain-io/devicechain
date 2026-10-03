// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBlockingDecodeSource builds a source whose decode outcome, success or failure, waits
// on release, and reports on entered when a decode has reached it.
func newBlockingDecodeSource(t *testing.T, entered chan<- struct{}, release <-chan struct{}) *MqttEventSource {
	t.Helper()
	block := func() {
		entered <- struct{}{}
		<-release
	}
	es, err := NewMqttEventSource("ext", "cid", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { block(); return nil },
		func(string, string, []byte, error) error { block(); return nil },
		nil, admitAllReadings, admitAll, alwaysOwns, func(err error) { t.Errorf("the source asked to end the process: %v", err) })
	require.NoError(t, err)
	return es
}

var termTestMessage = &fakeMqttMessage{topic: "inst-1/acme/devices/d1/events", payload: []byte(`{"device":"d1"}`)}

// 🔴 A STOPPED TERM HAS FINISHED WITH WHAT IT TOOK. An owned source releases its lease
// after Stop returns, and the next pod starts reading as soon as it has it, so Stop must
// not return while a decode worker is still handing on a message of this term: that
// message would be published after the next owner began. Here a decode is held open and
// Stop must wait for it.
func TestStopWaitsForTheDecodesAlreadyQueued(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	es := newBlockingDecodeSource(t, entered, release)
	es.initializeDecodeWorkers()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	es.onMessage(nil, termTestMessage)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the queued message never reached a decode worker")
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = es.ExecuteStop(context.Background())
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a decode worker was still handing on a message it had taken")
	case <-time.After(300 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return once the decode it was waiting for had finished")
	}
}

// paho's Disconnect returns on its own quiesce timer, not when its callbacks have
// finished, and an owned source's gate still answers yes until the lease is released after
// Stop. So a message callback can run after Stop has closed the decode queue; it must be
// dropped, not panic the process with a send on a closed channel.
func TestAMessageArrivingAfterStopIsDropped(t *testing.T) {
	received := 0
	es, err := NewMqttEventSource("ext", "cid", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) { received++ },
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, admitAll, alwaysOwns, func(err error) { t.Errorf("the source asked to end the process: %v", err) })
	require.NoError(t, err)
	es.initializeDecodeWorkers()
	require.NoError(t, es.ExecuteStop(context.Background()))

	assert.NotPanics(t, func() { es.onMessage(nil, termTestMessage) })
	assert.Zero(t, received, "a message dropped after Stop was counted as received")
}

// 🔴 A CONNECT IS ABANDONED WHEN ITS CONTEXT ENDS. An owned source starts each term under
// a context its lease cancels; a connect waited for without it would hold the term for
// paho's whole connect timeout (30 s) after the lease was lost, and then connect a second
// reader beside the pod that took the source over. This broker accepts the TCP connection
// and never answers the CONNECT.
func TestAConnectIsAbandonedWhenItsContextEnds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})

	es := newExternalMqttSourceAs(t, "devicechain:inst-1:ext:pod-a", "127.0.0.1", ln.Addr().(*net.TCPAddr).Port,
		"inst-1/+/devices/+/events", nil, func(err error) { t.Errorf("the source asked to end the process: %v", err) })
	require.NoError(t, es.Initialize(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- es.Start(ctx) }()

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the source never dialled the broker")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled, "the start failed, but not because its context ended")
	case <-time.After(5 * time.Second):
		t.Fatal("the start went on waiting for a CONNACK after its context ended")
	}
}
