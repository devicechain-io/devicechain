// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/stretchr/testify/require"
)

type deviceBackendFake struct {
	calls atomic.Int64
	mu    sync.Mutex
	plan  *DecoderPlan
	err   error
}

func (f *deviceBackendFake) DeviceDecoder(_ context.Context, _, _ string) (*DecoderPlan, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plan, f.err
}

func (f *deviceBackendFake) set(p *DecoderPlan, err error) {
	f.mu.Lock()
	f.plan, f.err = p, err
	f.mu.Unlock()
}

type dclock struct{ t time.Time }

func (c *dclock) now() time.Time { return c.t }

func TestDeviceDecodersCachePresentAndAbsent(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 3}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()

	l, err := d.Lookup(ctx, "acme", "m1")
	require.NoError(t, err)
	require.True(t, l.Plan.Present)
	require.False(t, l.Cached)
	l, _ = d.Lookup(ctx, "acme", "m1")
	require.True(t, l.Cached)
	require.EqualValues(t, 1, be.calls.Load())

	// A different tenant with the same device token is a different key.
	_, _ = d.Lookup(ctx, "other", "m1")
	require.EqualValues(t, 2, be.calls.Load())

	// Absent is cached too, and a nil plan is absent (never a panic).
	be.set(nil, nil)
	l, err = d.Lookup(ctx, "acme", "ghost")
	require.NoError(t, err)
	require.False(t, l.Plan.Present)
	_, _ = d.Lookup(ctx, "acme", "ghost")
	require.EqualValues(t, 3, be.calls.Load())

	// After the TTL it is asked again.
	ck.t = ck.t.Add(DefaultDecoderTTL + time.Second)
	be.set(&DecoderPlan{Present: true}, nil)
	l, _ = d.Lookup(ctx, "acme", "ghost")
	require.True(t, l.Plan.Present)
	require.EqualValues(t, 4, be.calls.Load())
}

func TestDeviceDecodersStaleOnErrorUntilStaleMax(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()
	_, _ = d.Lookup(ctx, "acme", "m1")

	be.set(nil, errors.New("down"))
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	l, err := d.Lookup(ctx, "acme", "m1")
	require.NoError(t, err)
	require.True(t, l.Stale)
	require.True(t, l.Plan.Present)

	ck.t = ck.t.Add(16 * time.Minute)
	_, err = d.Lookup(ctx, "acme", "m1")
	require.Error(t, err, "past StaleMax an outage is an error, not a remembered answer")

	// An unsupported device-management is never served from a stale entry as if it were an answer.
	be.set(&DecoderPlan{Present: true}, nil)
	_, _ = d.Lookup(ctx, "acme", "m2")
	be.set(nil, ErrDeviceDecoderUnsupported)
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	_, err = d.Lookup(ctx, "acme", "m2")
	require.ErrorIs(t, err, ErrDeviceDecoderUnsupported)
}

func TestDeviceDecodersErrorsAreNotCached(t *testing.T) {
	be := &deviceBackendFake{err: errors.New("down")}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{})
	for i := 0; i < 3; i++ {
		_, err := d.Lookup(context.Background(), "acme", "m1")
		require.Error(t, err)
	}
	require.EqualValues(t, 3, be.calls.Load())
	be.set(&DecoderPlan{Present: true}, nil)
	l, err := d.Lookup(context.Background(), "acme", "m1")
	require.NoError(t, err)
	require.True(t, l.Plan.Present)
}

func TestDeviceDecodersFillBudgetIsPerTenantThenPerReplica(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, TenantFillBurst: 5, TenantFillsPerSecond: 1, FillBurst: 8, FillsPerSecond: 1})
	ctx := context.Background()

	// A tenant flooding invented tokens is stopped at its own burst...
	var refused int
	for i := 0; i < 20; i++ {
		if _, err := d.Lookup(ctx, "evil", "d"+strconv.Itoa(i)); errors.Is(err, ErrDeviceDecoderBudget) {
			refused++
		}
	}
	require.Equal(t, 15, refused)
	require.EqualValues(t, 5, be.calls.Load())
	// ...and another tenant still has its own tokens and the replica's remaining three.
	for i := 0; i < 3; i++ {
		_, err := d.Lookup(ctx, "good", "d"+strconv.Itoa(i))
		require.NoError(t, err)
	}
	// The replica bucket is now empty: a fourth tenant is refused, though its own bucket is full.
	_, err := d.Lookup(ctx, "third", "d0")
	require.ErrorIs(t, err, ErrDeviceDecoderBudget)
	// A cache hit spends nothing.
	_, err = d.Lookup(ctx, "good", "d0")
	require.NoError(t, err)

	// Tokens return with time.
	ck.t = ck.t.Add(3 * time.Second)
	_, err = d.Lookup(ctx, "third", "d0")
	require.NoError(t, err)
}

func TestDeviceDecodersRefreshDeniedByBudgetIsServedStale(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, TenantFillBurst: 1, TenantFillsPerSecond: 0.0001, FillBurst: 100})
	ctx := context.Background()
	_, err := d.Lookup(ctx, "acme", "m1")
	require.NoError(t, err)
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	l, err := d.Lookup(ctx, "acme", "m1")
	require.NoError(t, err)
	require.True(t, l.Stale)
	require.EqualValues(t, 1, be.calls.Load())
}

func TestDeviceDecodersBoundedEntries(t *testing.T) {
	be := &deviceBackendFake{plan: &DecoderPlan{}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Entries: 32, TenantFillBurst: 1e6, FillBurst: 1e6})
	for i := 0; i < 500; i++ {
		_, err := d.Lookup(context.Background(), "acme", "d"+strconv.Itoa(i))
		require.NoError(t, err)
	}
	d.mu.RLock()
	n := len(d.entries)
	d.mu.RUnlock()
	require.LessOrEqual(t, n, 32)
}

func TestIsDeviceDecoderUnsupportedIsNarrow(t *testing.T) {
	yes := &svcclient.GraphQLError{Messages: []string{`Cannot query field "deviceDecoder" on type "Query".`}}
	require.True(t, IsDeviceDecoderUnsupported(yes))
	for _, e := range []error{
		errors.New("boom"),
		&svcclient.GraphQLError{Messages: []string{`Cannot query field "integrationDecoder" on type "Query".`}},
		&svcclient.GraphQLError{Messages: []string{`Cannot query field "deviceDecoder" on type "Query".`, "other"}},
		&svcclient.GraphQLError{},
	} {
		require.False(t, IsDeviceDecoderUnsupported(e), "%v", e)
	}
}

// The hit path is the hot path: a 64-goroutine hit stays under 1 us and one allocation (the key).
func BenchmarkDeviceDecodersHit(b *testing.B) {
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{TenantFillBurst: 1e6, FillBurst: 1e6})
	ctx := context.Background()
	for i := 0; i < 1024; i++ {
		_, _ = d.Lookup(ctx, "acme", "d"+strconv.Itoa(i))
	}
	b.ReportAllocs()
	b.SetParallelism(64)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := d.Lookup(ctx, "acme", "d"+strconv.Itoa(i&1023)); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// One cold device asked by many requests at once costs ONE backend call and ONE token: the waiters
// share the flight, so they must not each drain the tenant's bucket. The control is the same burst
// spread over distinct devices, which does spend a token per call.
func TestDeviceDecodersConcurrentMissesShareOneFillAndOneToken(t *testing.T) {
	gate := make(chan struct{})
	be := &blockingDeviceBackend{release: gate}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{TenantFillBurst: 3, TenantFillsPerSecond: 0.0001, FillBurst: 100})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Lookup(context.Background(), "acme", "one-cold-device")
			require.NoError(t, err)
		}()
	}
	for be.calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	require.EqualValues(t, 1, be.calls.Load())

	// Two tokens remain: two more distinct devices fill, the third is refused.
	for i := 0; i < 2; i++ {
		_, err := d.Lookup(context.Background(), "acme", "other"+strconv.Itoa(i))
		require.NoError(t, err)
	}
	_, err := d.Lookup(context.Background(), "acme", "other-last")
	require.ErrorIs(t, err, ErrDeviceDecoderBudget)
}

type blockingDeviceBackend struct {
	calls   atomic.Int64
	release chan struct{}
}

func (b *blockingDeviceBackend) DeviceDecoder(context.Context, string, string) (*DecoderPlan, error) {
	b.calls.Add(1)
	<-b.release
	return &DecoderPlan{Present: true}, nil
}
