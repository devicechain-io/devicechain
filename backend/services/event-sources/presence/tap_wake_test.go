// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/stretchr/testify/require"
)

// failingEmitter is a stream that will not take the write. Local rather than a flag on
// the shared recordingEmitter: that fixture is used by most of this package's tests, and
// a mode switch on it is one careless default away from silencing them.
type failingEmitter struct{ err error }

func (f failingEmitter) EmitPresence(context.Context, string, string, string, adapter.PresenceEvent) error {
	return f.err
}

func (f failingEmitter) EmitPresenceDemotion(context.Context, string, string, string, adapter.DemotionEvent) error {
	return f.err
}

// spyWaker records what the tap asked to wake.
type spyWaker struct {
	mu    sync.Mutex
	woken []wakeRequest
}

func (s *spyWaker) Wake(tenant, deviceToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.woken = append(s.woken, wakeRequest{tenant: tenant, deviceToken: deviceToken})
}

func (s *spyWaker) seen() []wakeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wakeRequest(nil), s.woken...)
}

// TestAConnectWakesTheDevicesCommands is the wake's whole purpose: the transport that
// owns the connection is the only thing that knows the device is back, and without this
// its withheld commands wait for command-delivery's periodic pass.
func TestAConnectWakesTheDevicesCommands(t *testing.T) {
	emitter := newRecordingEmitter()
	waker := &spyWaker{}
	tap := NewTap(testInstance, "mqtt1", emitter, nil, Metrics{}).WithWaker(waker)

	require.True(t, tap.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now()),
	}))

	got := waker.seen()
	require.Len(t, got, 1, "a device coming back must wake its withheld commands")
	require.Equal(t, "acme", got[0].tenant)
	require.Equal(t, "pump-1", got[0].deviceToken)
}

// 🔴 TestADisconnectWakesNothing. A wake on disconnect would release the commands of a
// device that has just LEFT, straight back into the delivery queue — where the sweep
// re-reads presence, sees it absent, and withholds them again. Harmless per event and
// unbounded in aggregate: every disconnect on the instance would buy a pointless round
// trip plus two status writes, and a flapping device would do it continuously.
func TestADisconnectWakesNothing(t *testing.T) {
	emitter := newRecordingEmitter()
	waker := &spyWaker{}
	tap := NewTap(testInstance, "mqtt1", emitter, nil, Metrics{}).WithWaker(waker)

	require.True(t, tap.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(false, 100, time.Now()),
	}))

	require.Empty(t, waker.seen(), "a disconnect must not wake anything")
}

// 🔴 TestARefusedTransitionWakesNothing. The admission gate refuses a deleted tenant and
// a tenant over its ceiling. Waking anyway would reach across that refusal into
// command-delivery — writing into a tenant being erased, which is the exact thing the
// gate on this path exists to stop.
func TestARefusedTransitionWakesNothing(t *testing.T) {
	emitter := newRecordingEmitter()
	waker := &spyWaker{}
	tap := NewTap(testInstance, "mqtt1", emitter,
		func(string, string, time.Time, bool) bool { return false }, Metrics{}).WithWaker(waker)

	require.False(t, tap.Apply(context.Background(), Transition{
		Tenant: "purged", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now()),
	}))

	require.Empty(t, waker.seen(), "a refused transition must not reach command-delivery")
}

// 🔴 TestAFailedEmitWakesNothing, and this is the ordering that is easy to get wrong.
//
// The wake races the emit: command-delivery re-reads presence when it dispatches, so a
// wake whose presence event never landed releases commands into a sweep that still reads
// the device as absent — which withholds them again. The round trip is wasted, and worse,
// it moves the wake counters, so the failure LOOKS like the feature working.
func TestAFailedEmitWakesNothing(t *testing.T) {
	waker := &spyWaker{}
	tap := NewTap(testInstance, "mqtt1", failingEmitter{errors.New("stream unavailable")}, nil,
		Metrics{}).WithWaker(waker)

	require.False(t, tap.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now()),
	}))

	require.Empty(t, waker.seen(),
		"a wake must not go out for a presence event that never reached the stream")
}

// TestATapWithNoWakerStillWorks. The waker is attached separately and legitimately
// absent — an instance that deploys no command-delivery, or one whose coordinate is not
// configured. Presence must not depend on it.
func TestATapWithNoWakerStillWorks(t *testing.T) {
	emitter := newRecordingEmitter()
	tap := NewTap(testInstance, "mqtt1", emitter, nil, Metrics{}) // no WithWaker

	require.True(t, tap.Apply(context.Background(), Transition{
		Tenant: "acme", DeviceToken: "pump-1",
		Event: presenceEventAt(true, 100, time.Now()),
	}))
	emitter.await(t, "presence still emitted with no waker", isDevice("acme", "pump-1", true))
}
