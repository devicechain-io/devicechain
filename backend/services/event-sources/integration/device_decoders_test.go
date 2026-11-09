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

// deviceBackendFake answers like device-management: a nil plan is "not authenticated".
type deviceBackendFake struct {
	calls atomic.Int64
	mu    sync.Mutex
	plan  *DecoderPlan
	err   error
	// good, when set, is the only credential id that authenticates; any other is refused.
	good string
}

func (f *deviceBackendFake) DeviceDecoder(_ context.Context, _, _ string, cred DeviceCredential) (*DecoderPlan, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.good != "" && cred.ID != f.good {
		return nil, nil
	}
	return f.plan, nil
}

func (f *deviceBackendFake) set(p *DecoderPlan, err error) {
	f.mu.Lock()
	f.plan, f.err = p, err
	f.mu.Unlock()
}

type dclock struct{ t time.Time }

func (c *dclock) now() time.Time { return c.t }

var cred1 = DeviceCredential{Type: "ACCESS_TOKEN", ID: "tok-1"}

func credN(i int) DeviceCredential {
	return DeviceCredential{Type: "ACCESS_TOKEN", ID: "tok-" + strconv.Itoa(i)}
}

func TestDeviceDecodersCachePresentAndAbsent(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true, Input: "FRAME", MaxReadings: 3}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()

	l, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	require.True(t, l.Authenticated)
	require.True(t, l.Plan.Present)
	require.False(t, l.Cached)
	l, _ = d.Lookup(ctx, "acme", "m1", cred1)
	require.True(t, l.Cached)
	require.EqualValues(t, 1, be.calls.Load())

	// A different tenant with the same device token is a different key.
	_, _ = d.Lookup(ctx, "other", "m1", cred1)
	require.EqualValues(t, 2, be.calls.Load())

	// An authenticated device with no decoder is cached too: present false, but authenticated.
	be.set(&DecoderPlan{}, nil)
	l, err = d.Lookup(ctx, "acme", "bare", cred1)
	require.NoError(t, err)
	require.True(t, l.Authenticated)
	require.False(t, l.Plan.Present)
	_, _ = d.Lookup(ctx, "acme", "bare", cred1)
	require.EqualValues(t, 3, be.calls.Load())

	// After the TTL it is asked again.
	ck.t = ck.t.Add(DefaultDecoderTTL + time.Second)
	be.set(&DecoderPlan{Present: true}, nil)
	l, _ = d.Lookup(ctx, "acme", "bare", cred1)
	require.True(t, l.Plan.Present)
	require.EqualValues(t, 4, be.calls.Load())
}

// 🔴 AN ANSWER REMEMBERED FOR ONE CREDENTIAL IS NEVER SERVED TO ANOTHER. The cache is keyed by who is
// asking: a request that presents a wrong credential for a device whose right credential was just
// vouched for is a miss, asked of device-management and refused.
func TestDeviceDecodersAnswerIsNotServedToAnotherCredential(t *testing.T) {
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}, good: "tok-1"}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{})
	ctx := context.Background()

	l, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	require.True(t, l.Authenticated, "the paired positive")

	bad, err := d.Lookup(ctx, "acme", "m1", credN(2))
	require.NoError(t, err)
	require.False(t, bad.Authenticated)
	require.False(t, bad.Cached, "a different credential is not a hit")
	require.EqualValues(t, 2, be.calls.Load())

	// The secret is part of who is asking too.
	pw := "pw"
	l, _ = d.Lookup(ctx, "acme", "m1", DeviceCredential{Type: "ACCESS_TOKEN", ID: "tok-1", Secret: &pw})
	require.False(t, l.Cached)
}

func TestDeviceCredentialDigestSeparatesFields(t *testing.T) {
	empty, pw := "", "x"
	seen := map[string]string{}
	for name, c := range map[string]DeviceCredential{
		"a|bc":        {Type: "a", ID: "bc"},
		"ab|c":        {Type: "ab", ID: "c"},
		"a|bc|empty":  {Type: "a", ID: "bc", Secret: &empty},
		"a|bc|x":      {Type: "a", ID: "bc", Secret: &pw},
		"a|b|c(x)":    {Type: "a", ID: "b", Secret: func() *string { s := "c"; return &s }()},
		"other-token": {Type: "a", ID: "bd"},
	} {
		dg := c.digest()
		require.Len(t, dg, 64)
		if prev, dup := seen[dg]; dup {
			t.Fatalf("%s and %s share a digest", prev, name)
		}
		seen[dg] = name
	}
	// The nil-secret form is the same credential as itself, and differs from an empty secret.
	require.Equal(t, DeviceCredential{Type: "a", ID: "bc"}.digest(), DeviceCredential{Type: "a", ID: "bc"}.digest())
	require.NotEqual(t, DeviceCredential{Type: "a", ID: "bc"}.digest(), DeviceCredential{Type: "a", ID: "bc", Secret: &empty}.digest())
}

// 🔴 NOTHING IS DISCLOSED TO A CALLER WHO DOES NOT HOLD THE CREDENTIAL: whether the backend would
// have answered a decoder, an absent decoder or nothing for the device, an unauthenticated lookup
// is one value, and a refusal is remembered only briefly and never stands in for an answer.
func TestDeviceDecodersUnauthenticatedIsOneAnswer(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	ctx := context.Background()
	var results []DeviceLookup
	for _, plan := range []*DecoderPlan{{Present: true, Input: "FRAME", MaxReadings: 9}, {}, nil} {
		be := &deviceBackendFake{plan: plan, good: "tok-right"}
		d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
		l, err := d.Lookup(ctx, "acme", "m1", cred1)
		require.NoError(t, err)
		results = append(results, l)
	}
	for _, l := range results {
		require.Equal(t, DeviceLookup{}, l, "an unauthenticated lookup carries nothing at all")
	}
}

func TestDeviceDecodersRefusalIsRememberedBrieflyAndRecovers(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}, good: "tok-fixed"}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		l, err := d.Lookup(ctx, "acme", "m1", cred1)
		require.NoError(t, err)
		require.False(t, l.Authenticated)
	}
	require.EqualValues(t, 1, be.calls.Load(), "a device retrying a bad credential costs one question per window")

	ck.t = ck.t.Add(DefaultRefusedTTL + time.Second)
	_, _ = d.Lookup(ctx, "acme", "m1", cred1)
	require.EqualValues(t, 2, be.calls.Load(), "asked again after the window")

	// A refusal never stands in for an answer when device-management is down.
	be.set(nil, errors.New("down"))
	ck.t = ck.t.Add(DefaultRefusedTTL + time.Second)
	_, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.Error(t, err)

	// And a credential that is fixed is accepted at the next window.
	be.set(&DecoderPlan{Present: true}, nil)
	l, err := d.Lookup(ctx, "acme", "m1", DeviceCredential{Type: "ACCESS_TOKEN", ID: "tok-fixed"})
	require.NoError(t, err)
	require.True(t, l.Authenticated)
}

// A credential revoked after it was vouched for is refused at the next refresh, and the stale
// authenticated answer is not what is served then.
func TestDeviceDecodersRevokedCredentialIsRefusedAtRefresh(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()
	l, _ := d.Lookup(ctx, "acme", "m1", cred1)
	require.True(t, l.Authenticated)

	be.set(nil, nil)
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	l, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	require.False(t, l.Authenticated)
	require.False(t, l.Stale)
}

func TestDeviceDecodersStaleOnErrorUntilStaleMax(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()
	_, _ = d.Lookup(ctx, "acme", "m1", cred1)

	be.set(nil, errors.New("down"))
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	l, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	require.True(t, l.Stale)
	require.True(t, l.Authenticated)
	require.True(t, l.Plan.Present)

	ck.t = ck.t.Add(16 * time.Minute)
	_, err = d.Lookup(ctx, "acme", "m1", cred1)
	require.Error(t, err, "past StaleMax an outage is an error, not a remembered answer")

	// An unsupported device-management is never served from a stale entry as if it were an answer.
	be.set(&DecoderPlan{Present: true}, nil)
	_, _ = d.Lookup(ctx, "acme", "m2", cred1)
	be.set(nil, ErrDeviceDecoderUnsupported)
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	_, err = d.Lookup(ctx, "acme", "m2", cred1)
	require.ErrorIs(t, err, ErrDeviceDecoderUnsupported)
}

func TestDeviceDecodersErrorsAreNotCached(t *testing.T) {
	be := &deviceBackendFake{err: errors.New("down")}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{})
	for i := 0; i < 3; i++ {
		_, err := d.Lookup(context.Background(), "acme", "m1", cred1)
		require.Error(t, err)
	}
	require.EqualValues(t, 3, be.calls.Load())
	be.set(&DecoderPlan{Present: true}, nil)
	l, err := d.Lookup(context.Background(), "acme", "m1", cred1)
	require.NoError(t, err)
	require.True(t, l.Plan.Present)
}

// 🔴 A COLD QUESTION IS NOT CHARGED TO ANY TENANT, because no tenant has been proven yet. A flood of
// invented credentials naming a victim tenant spends the replica's cold bucket and nothing of the
// victim's: the victim's already-vouched devices still refresh. The control is the same flood with
// the cold bucket large, which leaves the refresh untouched too (so the refusal below is the cold
// bucket's, not an artifact).
func TestDeviceDecodersFloodNamingATenantDoesNotSpendThatTenantsRefreshBudget(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}, good: "tok-1"}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, ColdFillBurst: 10, ColdFillsPerSecond: 0.0001,
		TenantFillBurst: 2, TenantFillsPerSecond: 0.0001, FillBurst: 100})
	ctx := context.Background()

	// The victim's device is vouched for.
	_, err := d.Lookup(ctx, "victim", "meter", cred1)
	require.NoError(t, err)
	// The attacker names the victim tenant with invented credentials and devices until refused.
	var refused int
	for i := 0; i < 50; i++ {
		_, err := d.Lookup(ctx, "victim", "d"+strconv.Itoa(i), credN(1000+i))
		if errors.Is(err, ErrDeviceDecoderBudget) {
			refused++
		}
	}
	require.Equal(t, 41, refused, "the cold bucket (10, one spent by the victim's own first contact) bounds the flood")

	// The victim's refreshes are untouched: both of its tokens are still there.
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	l, err := d.Lookup(ctx, "victim", "meter", cred1)
	require.NoError(t, err)
	require.True(t, l.Authenticated)
	require.False(t, l.Stale, "refreshed from the tenant's own bucket, not served stale")
	require.EqualValues(t, 1+9+1, be.calls.Load())
}

// A tenant's REFRESHES are bounded by its own bucket, so one tenant cannot starve another's, and
// by the replica's.
func TestDeviceDecodersRefreshBudgetIsPerAuthenticatedTenantThenPerReplica(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, ColdFillBurst: 1e6, TenantFillBurst: 2, TenantFillsPerSecond: 0.0001,
		FillBurst: 3, FillsPerSecond: 0.0001})
	ctx := context.Background()
	for _, tn := range []string{"noisy", "quiet"} {
		for i := 0; i < 4; i++ {
			_, err := d.Lookup(ctx, tn, "d"+strconv.Itoa(i), cred1)
			require.NoError(t, err)
		}
	}
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)
	be.calls.Store(0)

	// noisy refreshes four devices: two paid by its bucket, the others served stale.
	var stale int
	for i := 0; i < 4; i++ {
		l, err := d.Lookup(ctx, "noisy", "d"+strconv.Itoa(i), cred1)
		require.NoError(t, err)
		if l.Stale {
			stale++
		}
	}
	require.Equal(t, 2, stale)
	require.EqualValues(t, 2, be.calls.Load())
	// quiet still has its own two tokens, and the replica's one remaining.
	l, err := d.Lookup(ctx, "quiet", "d0", cred1)
	require.NoError(t, err)
	require.False(t, l.Stale)
	// The replica bucket (3) is now empty: quiet's second refresh is served stale though its own bucket has a token.
	l, err = d.Lookup(ctx, "quiet", "d1", cred1)
	require.NoError(t, err)
	require.True(t, l.Stale)
}

func TestDeviceDecodersColdBudgetRefusesWhenSpentAndRecovers(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, ColdFillBurst: 3, ColdFillsPerSecond: 1})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err := d.Lookup(ctx, "t", "d"+strconv.Itoa(i), cred1)
		require.NoError(t, err)
	}
	_, err := d.Lookup(ctx, "t", "d3", cred1)
	require.ErrorIs(t, err, ErrDeviceDecoderBudget)
	// A hit spends nothing.
	_, err = d.Lookup(ctx, "t", "d0", cred1)
	require.NoError(t, err)
	// Tokens return with time.
	ck.t = ck.t.Add(2 * time.Second)
	_, err = d.Lookup(ctx, "t", "d3", cred1)
	require.NoError(t, err)
}

// 🔴 A LOOKUP THAT LOST THE RACE FOR A FILL DOES NOT FILL AGAIN. A goroutine that read a miss can
// join the flight just after another flight stored the answer; it must find it, spend nothing and
// ask nothing.
func TestDeviceDecodersFlightRechecksTheCacheBeforeSpendingAToken(t *testing.T) {
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{ColdFillBurst: 1, ColdFillsPerSecond: 0.0001})
	ctx := context.Background()
	var ran atomic.Bool
	d.betweenMissAndFlight = func() {
		if ran.CompareAndSwap(false, true) {
			// Another request fills the same key between this one's miss and its flight.
			l, err := d.Lookup(ctx, "acme", "m1", cred1)
			require.NoError(t, err)
			require.True(t, l.Authenticated)
		}
	}
	l, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err, "with the bucket spent by the other flight, a second fill would fail on budget")
	require.True(t, l.Authenticated)
	require.EqualValues(t, 1, be.calls.Load())
}

func TestDeviceDecodersBoundedEntries(t *testing.T) {
	be := &deviceBackendFake{plan: &DecoderPlan{}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Entries: 32, ColdFillBurst: 1e6})
	for i := 0; i < 500; i++ {
		_, err := d.Lookup(context.Background(), "acme", "d"+strconv.Itoa(i), cred1)
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

// The hit path is the hot path: a 64-goroutine hit stays under 1 us and a few allocations (the key
// and the credential fingerprint).
func BenchmarkDeviceDecodersHit(b *testing.B) {
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{ColdFillBurst: 1e6})
	ctx := context.Background()
	for i := 0; i < 1024; i++ {
		_, _ = d.Lookup(ctx, "acme", "d"+strconv.Itoa(i), cred1)
	}
	b.ReportAllocs()
	b.SetParallelism(64)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := d.Lookup(ctx, "acme", "d"+strconv.Itoa(i&1023), cred1); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

// One cold device asked by many requests at once costs ONE backend call and ONE token: the waiters
// share the flight, so they must not each drain the bucket. The control is the same burst spread
// over distinct devices, which does spend a token per call.
func TestDeviceDecodersConcurrentMissesShareOneFillAndOneToken(t *testing.T) {
	gate := make(chan struct{})
	be := &blockingDeviceBackend{release: gate}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{ColdFillBurst: 3, ColdFillsPerSecond: 0.0001})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.Lookup(context.Background(), "acme", "one-cold-device", cred1)
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
		_, err := d.Lookup(context.Background(), "acme", "other"+strconv.Itoa(i), cred1)
		require.NoError(t, err)
	}
	_, err := d.Lookup(context.Background(), "acme", "other-last", cred1)
	require.ErrorIs(t, err, ErrDeviceDecoderBudget)
}

type blockingDeviceBackend struct {
	calls   atomic.Int64
	release chan struct{}
}

func (b *blockingDeviceBackend) DeviceDecoder(context.Context, string, string, DeviceCredential) (*DecoderPlan, error) {
	b.calls.Add(1)
	<-b.release
	return &DecoderPlan{Present: true}, nil
}

// 🔴 A STALE AUTHENTICATION NEVER STANDS IN OVER A NEWER REFUSAL. A lookup that read an authenticated
// entry, then lost the race to a flight that stored a refusal for the same credential, must not fall
// back to what it read when its own question then fails: that would answer "authenticated" for a
// credential the platform has just refused.
func TestDeviceDecodersStaleAuthenticationDoesNotOverrideANewerRefusal(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()
	l, _ := d.Lookup(ctx, "acme", "m1", cred1)
	require.True(t, l.Authenticated, "vouched for")
	ck.t = ck.t.Add(2 * DefaultDecoderTTL)

	var ran atomic.Bool
	d.betweenMissAndFlight = func() {
		if ran.CompareAndSwap(false, true) {
			// The credential is revoked and another request records the refusal...
			be.set(nil, nil)
			l, err := d.Lookup(ctx, "acme", "m1", cred1)
			require.NoError(t, err)
			require.False(t, l.Authenticated)
			// ...and by the time this request's own question is asked the refusal has aged out and
			// device-management is unreachable.
			ck.t = ck.t.Add(DefaultRefusedTTL + time.Second)
			be.set(nil, errors.New("down"))
		}
	}
	got, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.Error(t, err, "an error, not the stale authentication this request read before the refusal")
	require.False(t, got.Authenticated)
}

// Peek answers from the cache alone: nothing held is ok=false and asks no one; a fresh answer is
// served fresh; an authenticated answer past its TTL is served stale (to be refreshed); a refusal
// past its short life and anything past StaleMax are not served.
func TestDeviceDecodersPeekNeverAsksTheBackend(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true, Input: "FRAME"}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now})
	ctx := context.Background()

	_, _, ok := d.Peek("acme", "m1", cred1)
	require.False(t, ok)
	require.EqualValues(t, 0, be.calls.Load(), "a peek asks no one")

	_, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	l, fresh, ok := d.Peek("acme", "m1", cred1)
	require.True(t, ok)
	require.True(t, fresh)
	require.True(t, l.Authenticated && l.Plan.Present)

	ck.t = ck.t.Add(DefaultDecoderTTL + time.Second)
	l, fresh, ok = d.Peek("acme", "m1", cred1)
	require.True(t, ok, "stale is still served")
	require.False(t, fresh)
	require.True(t, l.Stale)

	ck.t = ck.t.Add(time.Hour)
	_, _, ok = d.Peek("acme", "m1", cred1)
	require.False(t, ok, "past StaleMax nothing is served")
	require.EqualValues(t, 1, be.calls.Load())

	// A remembered refusal is served only within its short life.
	be.set(nil, nil)
	_, err = d.Lookup(ctx, "acme", "ghost", cred1)
	require.NoError(t, err)
	l, fresh, ok = d.Peek("acme", "ghost", cred1)
	require.True(t, ok && fresh)
	require.False(t, l.Authenticated)
	ck.t = ck.t.Add(DefaultRefusedTTL + time.Second)
	_, _, ok = d.Peek("acme", "ghost", cred1)
	require.False(t, ok)
}

// A question about a device the broker proved is charged to ITS tenant's bucket and never spends the
// cold bucket that bounds a flood of invented credentials: with the cold bucket empty, a broker-asserted
// first contact is still answered, while a presented credential on its first contact is refused.
func TestABrokerAssertedFirstContactDoesNotSpendTheColdBucket(t *testing.T) {
	ck := &dclock{t: time.Unix(1000, 0)}
	be := &deviceBackendFake{plan: &DecoderPlan{Present: true}}
	d := NewDeviceDecoders(be, DeviceDecoderConfig{Now: ck.now, ColdFillBurst: 1, ColdFillsPerSecond: 0.0001})
	ctx := context.Background()

	// The one cold token goes to a presented credential.
	_, err := d.Lookup(ctx, "acme", "m1", cred1)
	require.NoError(t, err)
	_, err = d.Lookup(ctx, "acme", "m2", credN(2))
	require.ErrorIs(t, err, ErrDeviceDecoderBudget, "negative control: the cold bucket is empty")

	// Broker-asserted first contacts for many devices are all answered.
	asserted := DeviceCredential{Type: "BROKER_ASSERTED", AssertedByBroker: true}
	for i := 0; i < 50; i++ {
		l, err := d.Lookup(ctx, "acme", "dev-"+strconv.Itoa(i), asserted)
		require.NoError(t, err, "device %d", i)
		require.True(t, l.Authenticated)
	}
}
