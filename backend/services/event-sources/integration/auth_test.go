// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeBackend is a Backend with a scripted device-management behind it.
type fakeBackend struct {
	mu sync.Mutex
	// good maps "tenant/integration" to the one bearer token that authenticates.
	good map[string]string
	// disabled lists integrations whose token matches but that are switched off.
	disabled map[string]bool
	down     bool

	authCalls    atomic.Int64
	resolveCalls atomic.Int64
	failures     atomic.Int64
	undecoded    []undecodedCall
	resolve      func(scheme, value string) (Resolution, error)
}

type undecodedCall struct {
	tenant, integration, bearer string
	count                       int
	sample                      string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{good: map[string]string{"acme/cs": "right-token"}, disabled: map[string]bool{}}
}

func (f *fakeBackend) Auth(_ context.Context, tenant, integration, bearer string) (*Descriptor, error) {
	f.authCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("device-management is unreachable")
	}
	if want, ok := f.good[tenant+"/"+integration]; !ok || want != bearer {
		return nil, nil
	}
	d := &Descriptor{Token: integration, Type: ChirpStackV4, Enabled: !f.disabled[tenant+"/"+integration], Identity: "DEV_EUI"}
	return d, d.Prepare()
}

func (f *fakeBackend) Resolve(_ context.Context, _, _, _, scheme, value string) (Resolution, error) {
	f.resolveCalls.Add(1)
	if f.resolve != nil {
		return f.resolve(scheme, value)
	}
	return Resolution{Outcome: Resolved, DeviceToken: "dev-" + value}, nil
}

func (f *fakeBackend) RecordUndecoded(_ context.Context, tenant, integration, bearer string, count int, sample string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("device-management is unreachable")
	}
	f.undecoded = append(f.undecoded, undecodedCall{tenant, integration, bearer, count, sample})
	return nil
}

func (f *fakeBackend) RecordAuthFailure(context.Context, string, string) error {
	f.failures.Add(1)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newAuth(f *fakeBackend, c *clock, mutate ...func(*AuthConfig)) *Authenticator {
	cfg := AuthConfig{Now: c.now}
	for _, m := range mutate {
		m(&cfg)
	}
	return NewAuthenticator(f, cfg)
}

func TestARightTokenIsServedFromMemoryUntilItIsStale(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	ctx := context.Background()

	got, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.True(t, got.Descriptor.Enabled)
	require.False(t, got.Stale)
	require.EqualValues(t, 1, f.authCalls.Load())

	c.advance(59 * time.Second)
	_, err = a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.EqualValues(t, 1, f.authCalls.Load(), "inside the window device-management is not asked")

	c.advance(2 * time.Second)
	_, err = a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.EqualValues(t, 2, f.authCalls.Load(), "past the window it is asked again")
}

func TestAWrongOrUnknownTokenIsOneAnswer(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	for name, call := range map[string]func() error{
		"wrong token":         func() error { _, err := a.Check(context.Background(), "acme", "cs", "wrong"); return err },
		"unknown integration": func() error { _, err := a.Check(context.Background(), "acme", "nope", "right-token"); return err },
		"another tenant":      func() error { _, err := a.Check(context.Background(), "globex", "cs", "right-token"); return err },
	} {
		require.ErrorIs(t, call(), ErrUnauthorized, name)
	}
}

// A SPRAY OF BAD TOKENS. The bucket is taken BEFORE device-management is asked, so the spray is
// bounded to the burst however many requests it makes, and the integration's good traffic, which
// is served from memory, is untouched.
func TestASprayOfBadTokensIsBoundedBeforeItReachesDeviceManagementAndLeavesGoodTrafficAlone(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	ctx := context.Background()

	// The paired positive: the right token authenticates and is now remembered.
	_, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	before := f.authCalls.Load()

	unauthorized, limited := 0, 0
	for i := 0; i < 1000; i++ {
		_, err := a.Check(ctx, "acme", "cs", fmt.Sprintf("guess-%d", i))
		switch {
		case errors.Is(err, ErrUnauthorized):
			unauthorized++
		case errors.Is(err, ErrLimited):
			limited++
		default:
			t.Fatalf("unexpected answer %v", err)
		}
	}
	require.Equal(t, 30, unauthorized, "exactly the burst reached device-management")
	require.Equal(t, 970, limited)
	require.EqualValues(t, 30, f.authCalls.Load()-before, "device-management was asked 30 times, not 1000")

	got, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err, "the good token, remembered, is untouched by the spray")
	require.False(t, got.Stale)

	// Another integration of the same tenant has a bucket of its own.
	f.good["acme/other"] = "other-token"
	_, err = a.Check(ctx, "acme", "other", "other-token")
	require.NoError(t, err)

	// The bucket refills at one a second.
	c.advance(5 * time.Second)
	_, err = a.Check(ctx, "acme", "cs", "guess-again")
	require.ErrorIs(t, err, ErrUnauthorized)
}

// Past the freshness window and with the bucket spent, the integration's good token is served its
// last-known-good answer; with nothing held it is refused (429), because the credential was not
// even checked.
func TestAGoodTokenIsNotLockedOutByASprayThatEmptiedTheBucket(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	ctx := context.Background()
	_, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	for i := 0; i < 40; i++ {
		_, _ = a.Check(ctx, "acme", "cs", fmt.Sprintf("guess-%d", i))
	}
	c.advance(61 * time.Second) // stale now, but the bucket has refilled one token a second...
	for i := 0; i < 40; i++ {   // ...so spend it again
		_, _ = a.Check(ctx, "acme", "cs", fmt.Sprintf("more-%d", i))
	}
	got, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.True(t, got.Stale, "served last-known-good rather than refused")

	// A token never seen before, with the bucket spent, is refused unchecked.
	f.good["acme/cs"] = "rotated-token"
	_, err = a.Check(ctx, "acme", "cs", "rotated-token")
	require.ErrorIs(t, err, ErrLimited)
}

func TestARightTokenGivesItsChargeBack(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	for i := 0; i < 200; i++ {
		f.good[fmt.Sprintf("acme/i%d", i)] = "tok"
	}
	// 200 distinct right tokens, far more than the burst, all pass: each paid and was refunded.
	for i := 0; i < 200; i++ {
		_, err := a.Check(context.Background(), "acme", fmt.Sprintf("i%d", i), "tok")
		require.NoError(t, err)
	}
	// And the same integration, many cold-cache right tokens in a row.
	for i := 0; i < 100; i++ {
		tok := fmt.Sprintf("t%d", i)
		f.mu.Lock()
		f.good["acme/cs"] = tok
		f.mu.Unlock()
		_, err := a.Check(context.Background(), "acme", "cs", tok)
		require.NoError(t, err, "right token %d was charged as a bad one", i)
	}
}

func TestWhenDeviceManagementIsDownTheLastKnownGoodAnswerIsServedForFifteenMinutes(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	ctx := context.Background()
	_, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)

	f.mu.Lock()
	f.down = true
	f.mu.Unlock()

	c.advance(2 * time.Minute)
	got, err := a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.True(t, got.Stale)

	c.advance(12 * time.Minute) // 14 minutes in
	got, err = a.Check(ctx, "acme", "cs", "right-token")
	require.NoError(t, err)
	require.True(t, got.Stale)

	c.advance(2 * time.Minute) // 16 minutes in
	_, err = a.Check(ctx, "acme", "cs", "right-token")
	require.ErrorIs(t, err, ErrUnavailable, "past fifteen minutes there is no answer to serve")

	// With nothing ever held, an outage is 503 for a token that might well be right.
	_, err = a.Check(ctx, "acme", "cs", "another-right-token")
	require.ErrorIs(t, err, ErrUnavailable)

	// An outage is not the sender's fault: it never drained the bucket.
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	for i := 0; i < 30; i++ {
		_, err = a.Check(ctx, "acme", "cs", fmt.Sprintf("guess-%d", i))
		require.ErrorIs(t, err, ErrUnauthorized, "attempt %d", i)
	}
}

func TestADisabledIntegrationsTokenIsValidAndItsDescriptorSaysSo(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	f.disabled["acme/cs"] = true
	a := newAuth(f, c)
	got, err := a.Check(context.Background(), "acme", "cs", "right-token")
	require.NoError(t, err, "disabled is not a failed credential")
	require.False(t, got.Descriptor.Enabled)
	for i := 0; i < 100; i++ {
		_, err = a.Check(context.Background(), "acme", "cs", "right-token")
		require.NoError(t, err)
	}
}

func TestForgetDropsARememberedAnswer(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	_, err := a.Check(context.Background(), "acme", "cs", "right-token")
	require.NoError(t, err)
	a.Forget("acme", "cs", "right-token")
	_, err = a.Check(context.Background(), "acme", "cs", "right-token")
	require.NoError(t, err)
	require.EqualValues(t, 2, f.authCalls.Load())
}

func TestAFailedAuthenticationIsMarkedOnTheIntegrationAtMostOnceAMinute(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c)
	for i := 0; i < 5; i++ {
		_, _ = a.Check(context.Background(), "acme", "cs", fmt.Sprintf("guess-%d", i))
	}
	require.Eventually(t, func() bool { return f.failures.Load() == 1 }, time.Second, 5*time.Millisecond)
	c.advance(61 * time.Second)
	_, _ = a.Check(context.Background(), "acme", "cs", "guess-later")
	require.Eventually(t, func() bool { return f.failures.Load() == 2 }, time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return f.failures.Load() > 2 }, 50*time.Millisecond, 5*time.Millisecond)
}

// Integration names are chosen by the sender before any credential is checked, so the buckets
// are bounded: past the bound names share one, and a spray of invented names cannot grow memory.
func TestInventedNamesShareABoundedPoolOfBuckets(t *testing.T) {
	f, c := newFakeBackend(), newClock()
	a := newAuth(f, c, func(cfg *AuthConfig) { cfg.Buckets = 4 })
	for i := 0; i < 100; i++ {
		_, _ = a.Check(context.Background(), "acme", fmt.Sprintf("invented-%d", i), "x")
	}
	a.mu.Lock()
	n := len(a.buckets)
	a.mu.Unlock()
	require.LessOrEqual(t, n, 4)
	// The overflow bucket is bounded too: far fewer than 100 reached device-management.
	require.Less(t, f.authCalls.Load(), int64(100))
}
