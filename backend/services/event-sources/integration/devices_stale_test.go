// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A DEVICE-MANAGEMENT OUTAGE MUST NOT TURN A DEVICE THAT WAS RESOLVED INTO A 503 FIVE MINUTES IN.
// The route's token and decoder caches already serve their last real answer stale while
// device-management cannot be asked; the device cache was the one that did not, so a device last
// resolved more than five minutes before an outage had its uplinks refused while the same
// integration's other caches kept serving.

const staleEUI = "0101010101010101"

func resolveStale(t *testing.T, d *Devices) (Lookup, error) {
	t.Helper()
	return d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", staleEUI)
}

func TestAnExpiredDeviceIsServedStaleWhileDeviceManagementCannotBeAsked(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})

	got, err := resolveStale(t, d)
	require.NoError(t, err)
	require.False(t, got.Stale, "a fresh answer is not stale")

	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
	c.advance(DefaultPositiveTTL + time.Minute)
	got, err = resolveStale(t, d)
	require.NoError(t, err, "a device resolved six minutes ago is served while device-management is down")
	require.Equal(t, Resolved, got.Outcome)
	require.Equal(t, "dev-"+staleEUI, got.DeviceToken)
	require.True(t, got.Stale)
	require.True(t, got.Cached)
	require.EqualValues(t, 2, f.resolveCalls.Load(), "an expired entry is asked again before it is served stale")

	// device-management is back: the answer is fresh again and replaces the old one.
	f.resolve = nil
	got, err = resolveStale(t, d)
	require.NoError(t, err)
	require.False(t, got.Stale)
	require.False(t, got.Cached)
}

// The stale window is bounded, counted from when device-management last answered, so a long
// outage ends in 503s (the sender retries) rather than in serving a device forever.
func TestAStaleDeviceIsServedNoLongerThanStaleMax(t *testing.T) {
	require.Equal(t, 15*time.Minute, DefaultDeviceStaleMax, "the bound matches the token and decoder caches")
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})
	_, err := resolveStale(t, d)
	require.NoError(t, err)

	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
	c.advance(DefaultDeviceStaleMax - time.Second)
	got, err := resolveStale(t, d)
	require.NoError(t, err)
	require.True(t, got.Stale)

	c.advance(time.Second)
	_, err = resolveStale(t, d)
	require.Error(t, err, "past StaleMax the outage is a 503 again")
}

// A StaleMax shorter than the positive TTL would make an entry unservable while still fresh; it is
// raised to the TTL.
func TestStaleMaxIsNeverShorterThanTheTTL(t *testing.T) {
	cfg := DeviceConfig{Positive: 10 * time.Minute, StaleMax: time.Minute}.withDefaults()
	require.Equal(t, 10*time.Minute, cfg.StaleMax)
	cfg = DeviceConfig{Positive: time.Minute, StaleMax: 2 * time.Minute}.withDefaults()
	require.Equal(t, 2*time.Minute, cfg.StaleMax)
}

// 🔴 A DEFINITIVE ANSWER IS NEVER TURNED INTO AN ACCEPT. Only a failure to ask is served stale.
// When device-management answers that the identifier is no longer registered, that the
// integration is disabled or that its token no longer holds, that answer is returned, and the old
// positive entry is gone: a later outage cannot bring it back.
func TestADefinitiveAnswerReplacesAStalePositiveOne(t *testing.T) {
	for _, outcome := range []Outcome{Unregistered, Disabled, Unauthenticated} {
		f, c := newFakeBackend(), newClock()
		d := NewDevices(f, DeviceConfig{Now: c.now})
		_, err := resolveStale(t, d)
		require.NoError(t, err)

		c.advance(DefaultPositiveTTL + time.Minute)
		f.resolve = func(string, string) (Resolution, error) { return Resolution{Outcome: outcome}, nil }
		got, err := resolveStale(t, d)
		require.NoError(t, err)
		require.Equal(t, outcome, got.Outcome, "outcome %d", outcome)
		require.False(t, got.Stale)

		// The negative answer itself expires, and then device-management goes down.
		c.advance(2 * time.Minute)
		f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
		_, err = resolveStale(t, d)
		require.Error(t, err, "outcome %d: the positive entry it replaced came back during an outage", outcome)
	}
}

// Only a POSITIVE answer is served stale. A remembered "unregistered" past its minute is asked
// again, and a failure to ask is a 503 the sender retries, never a drop of an uplink for a device
// that may have been registered since.
func TestAnExpiredUnregisteredAnswerIsNotServedStale(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	f.resolve = func(string, string) (Resolution, error) { return Resolution{Outcome: Unregistered}, nil }
	d := NewDevices(f, DeviceConfig{Now: c.now})
	_, err := resolveStale(t, d)
	require.NoError(t, err)

	c.advance(2 * time.Minute)
	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
	_, err = resolveStale(t, d)
	require.Error(t, err)
}

// Forget is how the route says the credential no longer holds; a stale entry it dropped is not
// served.
func TestAForgottenDeviceIsNotServedStale(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})
	_, err := resolveStale(t, d)
	require.NoError(t, err)
	d.Forget("acme", "cs")
	c.advance(DefaultPositiveTTL + time.Minute)
	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
	_, err = resolveStale(t, d)
	require.Error(t, err)
}

// At capacity, the eviction that makes room must not throw away a positive entry that is past its
// TTL but still servable stale. It removes one entry, picked in map order, so it is run often
// enough that an eviction treating both as expired cannot pass by luck.
func TestEvictionKeepsAServablePositiveEntry(t *testing.T) {
	for i := 0; i < 32; i++ {
		evictionKeepsAServablePositiveEntry(t)
	}
}

func evictionKeepsAServablePositiveEntry(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now, Entries: 2})
	_, err := resolveStale(t, d) // positive, servable until StaleMax
	require.NoError(t, err)
	f.resolve = func(string, string) (Resolution, error) { return Resolution{Outcome: Unregistered}, nil }
	_, err = d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0202020202020202")
	require.NoError(t, err)

	// Past both TTLs: the unregistered answer can never be served again, the positive one can.
	c.advance(DefaultPositiveTTL + time.Minute)
	f.resolve = nil
	_, err = d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0303030303030303")
	require.NoError(t, err)

	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("connection refused") }
	got, err := resolveStale(t, d)
	require.NoError(t, err, "the servable positive entry was evicted")
	require.True(t, got.Stale)
}
