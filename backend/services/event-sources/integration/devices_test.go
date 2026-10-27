// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAResolvedDeviceIsRememberedForFiveMinutes(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})
	ctx := context.Background()

	got, err := d.Resolve(ctx, "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
	require.NoError(t, err)
	require.Equal(t, Resolved, got.Outcome)
	require.Equal(t, "dev-0101010101010101", got.DeviceToken)
	require.False(t, got.Cached)

	require.Equal(t, 5*time.Minute, DefaultPositiveTTL, "the bound on a stale alias is a documented number")
	c.advance(4 * time.Minute)
	got, err = d.Resolve(ctx, "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
	require.NoError(t, err)
	require.True(t, got.Cached)
	require.EqualValues(t, 1, f.resolveCalls.Load())

	c.advance(2 * time.Minute)
	_, err = d.Resolve(ctx, "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
	require.NoError(t, err)
	require.EqualValues(t, 2, f.resolveCalls.Load())
}

// An unregistered device is recorded as a sighting by the call that finds it unregistered, and
// the negative answer is remembered, so a device that keeps transmitting costs one sighting a
// minute and not one per uplink.
func TestAnUnregisteredDeviceCostsOneSightingAMinute(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	f.resolve = func(string, string) (Resolution, error) { return Resolution{Outcome: Unregistered}, nil }
	d := NewDevices(f, DeviceConfig{Now: c.now})
	for i := 0; i < 500; i++ {
		got, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
		require.NoError(t, err)
		require.Equal(t, Unregistered, got.Outcome)
	}
	require.EqualValues(t, 1, f.resolveCalls.Load())

	c.advance(61 * time.Second)
	_, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
	require.NoError(t, err)
	require.EqualValues(t, 2, f.resolveCalls.Load(), "a minute later it is recorded again")

	// A different identifier is its own row.
	_, err = d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0202020202020202")
	require.NoError(t, err)
	require.EqualValues(t, 3, f.resolveCalls.Load())
}

// A stampede of uplinks from one unknown device before the first answer arrives is one call.
func TestConcurrentLookupsOfOneDeviceAreOneCall(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	release := make(chan struct{})
	f.resolve = func(string, string) (Resolution, error) {
		<-release
		return Resolution{Outcome: Unregistered}, nil
	}
	d := NewDevices(f, DeviceConfig{Now: c.now})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
			require.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool { return f.resolveCalls.Load() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, f.resolveCalls.Load())
}

// DISABLED and UNAUTHENTICATED are about the credential, not the device: neither is remembered,
// and an error is never cached as an answer.
func TestOnlyDeviceAnswersAreRemembered(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})
	for _, outcome := range []Outcome{Disabled, Unauthenticated} {
		f.resolveCalls.Store(0)
		f.resolve = func(string, string) (Resolution, error) { return Resolution{Outcome: outcome}, nil }
		for i := 0; i < 3; i++ {
			got, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
			require.NoError(t, err)
			require.Equal(t, outcome, got.Outcome)
		}
		require.EqualValues(t, 3, f.resolveCalls.Load(), "outcome %d was cached", outcome)
	}

	f.resolveCalls.Store(0)
	f.resolve = func(string, string) (Resolution, error) { return Resolution{}, errors.New("boom") }
	for i := 0; i < 3; i++ {
		_, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", "0101010101010101")
		require.Error(t, err)
	}
	require.EqualValues(t, 3, f.resolveCalls.Load())
}

func TestForgetDropsEveryAnswerOfAnIntegration(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	d := NewDevices(f, DeviceConfig{Now: c.now})
	for _, v := range []string{"0101010101010101", "0202020202020202"} {
		_, err := d.Resolve(context.Background(), "acme", "cs", "tok", "lorawan.deveui", v)
		require.NoError(t, err)
	}
	_, err := d.Resolve(context.Background(), "acme", "other", "tok", "lorawan.deveui", "0101010101010101")
	require.NoError(t, err)
	d.Forget("acme", "cs")
	f.resolveCalls.Store(0)
	for _, in := range []string{"cs", "other"} {
		_, err = d.Resolve(context.Background(), "acme", in, "tok", "lorawan.deveui", "0101010101010101")
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, f.resolveCalls.Load(), "cs was dropped, other kept")
}

func TestTheUndecodedMarkerIsWrittenAtOnceThenInBatches(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	u := NewUndecoded(f, time.Minute, c.now, nil)

	u.Note("acme", "cs", "tok", "aa")
	waitForCalls(t, f, 1)
	require.Equal(t, undecodedCall{"acme", "cs", "tok", 1, "aa"}, f.undecoded[0])

	// Inside the interval they accumulate without a write.
	for i := 0; i < 10; i++ {
		u.Note("acme", "cs", "tok", "bb")
	}
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	require.Len(t, f.undecoded, 1)
	f.mu.Unlock()

	c.advance(61 * time.Second)
	u.flushDue()
	waitForCalls(t, f, 2)
	require.Equal(t, undecodedCall{"acme", "cs", "tok", 10, "bb"}, f.undecoded[1])

	// Nothing is left to write, and the bearer token is not kept.
	u.mu.Lock()
	e := u.pending["acme\x00cs"]
	u.mu.Unlock()
	require.Zero(t, e.count)
	require.Empty(t, e.bearer)
	c.advance(61 * time.Second)
	u.flushDue()
	f.mu.Lock()
	require.Len(t, f.undecoded, 2)
	f.mu.Unlock()
}

func TestAFailedMarkerWriteIsKeptForTheNextAttempt(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	u := NewUndecoded(f, time.Minute, c.now, nil)
	u.Note("acme", "cs", "tok", "aa")
	u.Note("acme", "cs", "tok", "aa")
	time.Sleep(50 * time.Millisecond)

	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	c.advance(61 * time.Second)
	u.flushDue()
	waitForCalls(t, f, 1)
	require.Equal(t, 2, f.undecoded[0].count, "nothing counted was lost to the failed write")
}

func TestTheUndecodedTableIsBounded(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	// Writes fail, so every tally stays pending: a tally whose write succeeded is idle and is
	// evicted to make room, which is correct but would make this count depend on timing.
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	dropped := 0
	u := NewUndecoded(f, time.Hour, c.now, func() { dropped++ })
	u.max = 3
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		u.Note("acme", name, "tok", "")
	}
	require.Equal(t, 2, dropped, "the names past the bound are counted, not held")
}

func waitForCalls(t *testing.T, f *fakeBackend, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.undecoded) >= n
	}, 2*time.Second, 5*time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.undecoded, n)
}
