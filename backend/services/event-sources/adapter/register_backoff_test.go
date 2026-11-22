// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var autoRegister = IngestPolicy{AutoRegister: true, DeviceTypeToken: "missing-type"}

// failingCreates answers every lookup with a miss and every create with an error: a device type
// that passes the grammar check but names no existing type.
func failingCreates() *fakeGraphQL {
	return &fakeGraphQL{responder: func(query string, _ map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			return lookupMiss(), nil
		}
		return nil, errors.New("device type not found")
	}}
}

func registrarAt(gql *fakeGraphQL, now *time.Time) *Registrar {
	r := NewRegistrar(gql, "url", "lw-", nil)
	r.now = func() time.Time { return *now }
	return r
}

// A create that failed and left no device puts that device in a short backoff: a message inside
// the window is dropped WITHOUT another create (and with no error the caller would retry); after
// the window, one create is tried again.
func TestAFailedAutoRegisterBacksOffThatDevice(t *testing.T) {
	gql := failingCreates()
	now := time.Unix(1_700_000_000, 0)
	r := registrarAt(gql, &now)
	ctx := context.Background()

	_, outcome, err := r.Resolve(ctx, "acme", "dev-a", autoRegister)
	require.Error(t, err, "the first failure is still returned for the caller to see and retry")
	assert.Equal(t, ResolveDropped, outcome)
	require.Equal(t, 1, gql.creates)

	now = now.Add(registerBackoffBase / 2)
	_, outcome, err = r.Resolve(ctx, "acme", "dev-a", autoRegister)
	require.NoError(t, err, "inside the window the message is a definitive drop, not a retry")
	assert.Equal(t, ResolveRegisterBackoff, outcome)
	assert.False(t, outcome.Ingestible())
	assert.Equal(t, 1, gql.creates, "no create inside the backoff window")

	now = now.Add(registerBackoffBase)
	_, _, err = r.Resolve(ctx, "acme", "dev-a", autoRegister)
	require.Error(t, err)
	assert.Equal(t, 2, gql.creates, "after the window, exactly one more create")
}

// The window grows while the device keeps failing, up to a cap, and a success clears it.
func TestTheAutoRegisterBackoffGrowsToItsCapAndClearsOnSuccess(t *testing.T) {
	fail := true
	gql := &fakeGraphQL{responder: func(query string, _ map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			return lookupMiss(), nil
		}
		if fail {
			return nil, errors.New("device type not found")
		}
		return createHit("lw-x"), nil
	}}
	now := time.Unix(1_700_000_000, 0)
	r := registrarAt(gql, &now)
	ctx := context.Background()

	want := registerBackoffBase
	for i := 0; i < 12; i++ {
		_, _, err := r.Resolve(ctx, "acme", "dev-b", autoRegister)
		require.Error(t, err)
		until, blocked := r.backoff.blocked("acme", "dev-b", now)
		require.True(t, blocked)
		assert.Equal(t, want, until.Sub(now), "failure %d", i+1)
		now = until // the next message arrives just as the window ends
		if want *= 2; want > registerBackoffMax {
			want = registerBackoffMax
		}
	}

	fail = false
	_, outcome, err := r.Resolve(ctx, "acme", "dev-b", autoRegister)
	require.NoError(t, err)
	assert.Equal(t, ResolveCreated, outcome)
	_, blocked := r.backoff.blocked("acme", "dev-b", now)
	assert.False(t, blocked, "a success clears the backoff")
	assert.Zero(t, r.backoff.size(), "a success removes the device's entry, so a later failure starts from the base")
}

// A tenant found deleted takes its devices' backoff entries with it, through Resolve.
func TestResolveForADeletedTenantDropsItsAutoRegisterBackoff(t *testing.T) {
	gql := failingCreates()
	now := time.Unix(1_700_000_000, 0)
	deleted := false
	r := NewRegistrar(gql, "url", "lw-", func(string) bool { return deleted })
	r.now = func() time.Time { return now }
	_, _, _ = r.Resolve(context.Background(), "acme", "dev-a", autoRegister)
	require.Equal(t, 1, r.backoff.size())
	deleted = true
	_, outcome, err := r.Resolve(context.Background(), "acme", "dev-a", autoRegister)
	require.NoError(t, err)
	assert.Equal(t, ResolveTenantGone, outcome)
	assert.Zero(t, r.backoff.size())
}

// 🔴 ONE DEVICE'S BACKOFF NEVER HOLDS ANOTHER BACK: the same external id in another tenant, and
// another device in the same tenant, are still created on their first message.
func TestAnAutoRegisterBackoffIsPerDevice(t *testing.T) {
	gql := failingCreates()
	now := time.Unix(1_700_000_000, 0)
	r := registrarAt(gql, &now)
	ctx := context.Background()

	_, _, _ = r.Resolve(ctx, "acme", "dev-a", autoRegister)
	require.Equal(t, 1, gql.creates)
	_, _, _ = r.Resolve(ctx, "acme", "dev-c", autoRegister)
	_, _, _ = r.Resolve(ctx, "globex", "dev-a", autoRegister)
	assert.Equal(t, 3, gql.creates, "other devices and other tenants are not held back")
}

// The burst path is unchanged: a create that fails because a concurrent caller registered the
// device resolves to the winner and puts nothing in backoff.
func TestACreateLostToAConcurrentBurstIsNotBackedOff(t *testing.T) {
	lookups := 0
	gql := &fakeGraphQL{responder: func(query string, _ map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			lookups++
			if lookups == 1 {
				return lookupMiss(), nil
			}
			return lookupHit("lw-winner"), nil
		}
		return nil, errors.New("duplicate key value violates unique constraint")
	}}
	now := time.Unix(1_700_000_000, 0)
	r := registrarAt(gql, &now)

	tok, outcome, err := r.Resolve(context.Background(), "acme", "dev-a", autoRegister)
	require.NoError(t, err)
	assert.Equal(t, "lw-winner", tok)
	assert.Equal(t, ResolveFound, outcome)
	_, blocked := r.backoff.blocked("acme", "dev-a", now)
	assert.False(t, blocked)
}

// The backoff table is bounded: a flood of distinct failing ids never grows it past its cap, and
// an id that finds the table full gets no backoff (today's behaviour) rather than evicting one.
func TestTheAutoRegisterBackoffTableIsBounded(t *testing.T) {
	b := newRegisterBackoff()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < registerBackoffMaxEntries+50; i++ {
		b.fail("acme", fmt.Sprintf("dev-%d", i), now)
	}
	assert.Equal(t, registerBackoffMaxEntries, b.size())
	_, blocked := b.blocked("acme", fmt.Sprintf("dev-%d", registerBackoffMaxEntries+10), now)
	assert.False(t, blocked, "an id that did not fit is simply not backed off")

	// Entries long past their window are forgotten, so the table drains on its own.
	later := now.Add(registerBackoffBase + registerBackoffMax + time.Second)
	b.fail("acme", "fresh", later)
	assert.Equal(t, 1, b.size(), "expired entries were pruned to make room")
}

// A deleted tenant's backoff entries go with it.
func TestATenantGoneClearsItsAutoRegisterBackoff(t *testing.T) {
	b := newRegisterBackoff()
	now := time.Unix(1_700_000_000, 0)
	b.fail("acme", "dev-a", now)
	b.fail("globex", "dev-a", now)
	b.invalidateTenant("acme")
	_, blocked := b.blocked("acme", "dev-a", now)
	assert.False(t, blocked)
	_, blocked = b.blocked("globex", "dev-a", now)
	assert.True(t, blocked)
}

// A failed create whose re-look-up ALSO failed says nothing about the create (device-management may
// be down): the error is returned for a retry and nothing is backed off.
func TestAFailedReLookupBacksNothingOff(t *testing.T) {
	lookups := 0
	gql := &fakeGraphQL{responder: func(query string, _ map[string]any) (any, error) {
		if strings.Contains(query, "devicesByExternalId") {
			lookups++
			if lookups == 1 {
				return lookupMiss(), nil
			}
			return nil, errors.New("device-management unavailable")
		}
		return nil, errors.New("device-management unavailable")
	}}
	now := time.Unix(1_700_000_000, 0)
	r := registrarAt(gql, &now)
	_, _, err := r.Resolve(context.Background(), "acme", "dev-a", autoRegister)
	require.Error(t, err)
	_, blocked := r.backoff.blocked("acme", "dev-a", now)
	assert.False(t, blocked)
}
