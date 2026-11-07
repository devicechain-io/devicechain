// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lifecycleWith builds a resolver over a canned fetch. It bypasses
// NewTenantLifecycleResolver only in what it fetches WITH — the resolver, its default
// and its cache mechanics are the real ones, so the fail-open behaviour under test is
// the shipped behaviour and not a re-statement of it.
func lifecycleWith(fetch func(context.Context, string) (string, error)) *TenantLifecycleResolver {
	return &TenantLifecycleResolver{
		tenantResolver: newTenantResolver(fetch, LifecycleActive, "tenant-lifecycle-test"),
	}
}

// fixedLifecycle returns a fetch serving one state, counting calls.
type fixedLifecycle struct {
	mu    sync.Mutex
	calls int
	state string
	err   error
}

func (f *fixedLifecycle) fetch(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.state, f.err
}

func (f *fixedLifecycle) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// A purging tenant reads deleted once the out-of-band refresh has landed. This is the
// gate doing its job at all — every other test here is about what it must NOT do.
func TestLifecycleResolverReportsAPurgingTenantDeleted(t *testing.T) {
	f := &fixedLifecycle{state: "purging"}
	r := lifecycleWith(f.fetch)

	assert.False(t, r.Deleted("acme"), "an unfetched tenant must not read deleted")
	assert.Eventually(t, func() bool { return r.Deleted("acme") },
		time.Second, 5*time.Millisecond, "a purging tenant should read deleted once cached")
}

// An active tenant never reads deleted, cached or not. The negative control for the
// test above: without it, a Deleted that returned true unconditionally would pass.
func TestLifecycleResolverNeverReportsAnActiveTenantDeleted(t *testing.T) {
	f := &fixedLifecycle{state: LifecycleActive}
	r := lifecycleWith(f.fetch)

	assert.False(t, r.Deleted("acme"))
	require.Eventually(t, func() bool { return f.callCount() >= 1 },
		time.Second, 5*time.Millisecond, "the refresh should have run")
	assert.False(t, r.Deleted("acme"), "an active tenant must never read deleted")
}

// 🔴 THE FAIL-OPEN, pinned as a decision rather than left as whatever the cache
// happens to do. An unreachable user-management must not read as "everything is
// deleted" — that would take the entire instance's device plane down with the
// authority, which is the availability regression the resolver's comment argues
// against. ADR-077's per-area fence is the correctness path; this gate only stops the
// bleeding early.
func TestLifecycleResolverFailsOpenWhenTheAuthorityIsUnreachable(t *testing.T) {
	f := &fixedLifecycle{err: errors.New("user-management unreachable")}
	r := lifecycleWith(f.fetch)

	assert.False(t, r.Deleted("acme"))
	require.Eventually(t, func() bool { return f.callCount() >= 1 },
		time.Second, 5*time.Millisecond, "the refresh should have been attempted")
	assert.False(t, r.Deleted("acme"),
		"a failed fetch must leave the tenant readable, not lock it out")
}

// The two unusual wire states, read at the decision point. They go OPPOSITE ways and
// that is the point of the function, so both are pinned:
//
//   - empty is a broken answer and must fail open, or the naive `!= "active"` reading
//     locks out every tenant on the instance rather than none. Unreachable through the
//     schema today, which is why it is pinned rather than assumed away.
//   - unrecognised must fail CLOSED, because ADR-077 may add a state between the cut and
//     reclamation and a reader too old to name it must not conclude the tenant is fine.
func TestLifecycleDeletedReadsTheUnusualStates(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{LifecycleActive, false},
		{"purging", true},
		{"", false},           // broken answer → fail open
		{"quarantined", true}, // a state this build is too old to name → fail closed
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, lifecycleDeleted(c.state), "lifecycleDeleted(%q)", c.state)
	}
}

// And the same two, through the real resolver — so the rule above is the one the gate
// actually applies rather than a parallel implementation of it.
//
// 🔴 It waits on the value being CACHED, not on the fetch being CALLED, and the
// difference is the whole reliability of this test. callCount rises when the fetch is
// entered; the cache is written after it returns. Waiting on the call left the
// "quarantined" leg racing a store it had not observed (red on a loaded box), and left
// the "" leg vacuous in the other direction — before the store, the default "active"
// also reads not-deleted, so it would have passed without the cache ever landing.
// resolveOK's bool is exactly "a real fetched value backs this", so it is the event to
// wait for.
func TestLifecycleResolverAppliesTheUnusualStates(t *testing.T) {
	for _, c := range []struct {
		state string
		want  bool
	}{{"", false}, {"quarantined", true}} {
		f := &fixedLifecycle{state: c.state}
		r := lifecycleWith(f.fetch)
		r.Deleted("acme") // trigger the out-of-band refresh; nothing is fetched until asked
		require.Eventuallyf(t, func() bool { _, cached := resolvedOK(r.tenantResolver, "acme"); return cached },
			time.Second, 5*time.Millisecond, "state %q should have reached the cache", c.state)
		assert.Equalf(t, c.want, r.Deleted("acme"), "resolver reading of state %q", c.state)
	}
}

// 🔴 THE FETCH PATH, which nothing above reaches.
//
// lifecycleWith injects a canned fetch, so every test above exercises the CACHE and the
// READING but not the query that feeds them. The concrete svcclient.Client is not an
// interface, so the query string and the decode shape are the only parts of the real
// fetch a unit test can hold — and both fail in the same silent direction: a wrong field
// name errors every fetch, a wrong json tag decodes to "", and this resolver's fail-open
// turns either into "no tenant is ever deleted" with one WARN line per refresh. That is
// the whole gate off, instance-wide, looking healthy. governanceQuery and the limits
// decode are pinned for exactly this reason; this is the lifecycle's equivalent.
func TestPurgeStateQueryNamesTheContractField(t *testing.T) {
	assert.Equal(t, `query { tenantGovernance { purgeState } }`, purgeStateQuery,
		"user-management serves this field under this exact name (pinned there too)")
}

func TestPurgeStateResponseDecodesARealAnswer(t *testing.T) {
	// The data object svcclient hands to the decoder, verbatim in shape.
	var out purgeStateResponse
	require.NoError(t, json.Unmarshal([]byte(`{"tenantGovernance":{"purgeState":"purging"}}`), &out))
	assert.Equal(t, "purging", out.TenantGovernance.PurgeState)
	assert.True(t, lifecycleDeleted(out.TenantGovernance.PurgeState),
		"a purging answer must survive the decode as a refusal")

	// And the failure this pins: a decode that silently yields "" reads as ACTIVE, so a
	// mis-tagged field would disable the gate rather than break loudly.
	var empty purgeStateResponse
	require.NoError(t, json.Unmarshal([]byte(`{"tenantGovernance":{"somethingElse":"purging"}}`), &empty))
	assert.False(t, lifecycleDeleted(empty.TenantGovernance.PurgeState),
		"this is the silent-failure direction the assertion above exists to prevent")
}

// The gate helper's config gating: unconfigured means "off", never a startup failure and
// never a half-built client pointed at http://:0/graphql. Each of the three coordinates
// is checked, because a config document predating this feature carries none of them.
func TestNewTenantLifecycleGateIsOffWhenUnconfigured(t *testing.T) {
	full := config.UserManagementConfiguration{Hostname: "user-management", Port: 8080, InternalPort: 8080}
	for _, c := range []struct {
		name   string
		cfg    config.UserManagementConfiguration
		secret string
	}{
		{"no secret", full, ""},
		{"no hostname", config.UserManagementConfiguration{Port: 8080}, "s3cret"},
		{"no port", config.UserManagementConfiguration{Hostname: "user-management"}, "s3cret"},
	} {
		assert.Nilf(t, NewTenantLifecycleGate(c.cfg, c.secret, "test", nil), "gate with %s", c.name)
	}

	// The control: fully configured builds a gate. Without it, a helper that returned nil
	// unconditionally would satisfy every case above and leave every service carrying this
	// gate ungated. (Deliberately not counted — the count has been wrong twice; the set is
	// `grep -rl NewTenantLifecycleGate backend/services`.)
	gate := NewTenantLifecycleGate(full, "s3cret", "test", nil)
	require.NotNil(t, gate)
	assert.False(t, gate("acme"),
		"a freshly built gate has fetched nothing yet and must fail open, not refuse")
}

// manualClock is a clock the test advances by hand; the resolver reads it for every freshness
// decision, so no assertion below depends on elapsed real time.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// quiesce returns once no refresh is in flight, i.e. once every fetch started so far has landed
// in the cache. It waits on the resolver's own bookkeeping rather than on a delay, so what is
// asserted afterwards is the settled state, whatever the machine's speed.
func quiesce(t *testing.T, r *TenantLifecycleResolver) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.inflight)
		r.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a refresh never finished")
		}
		runtime.Gosched()
	}
}

// lifecycleOnClock is lifecycleWith on a manual clock, with refreshes not rate-bound.
func lifecycleOnClock(fetch func(context.Context, string) (string, error)) (*TenantLifecycleResolver, *manualClock) {
	clock := &manualClock{now: time.Unix(1_700_000_000, 0)}
	r := lifecycleWith(fetch)
	r.Configure(ResolverOptions{TTL: time.Minute, NegativeTTL: time.Minute, Clock: clock.Now,
		RefreshesPerSecond: 1000, RefreshBurst: 1000})
	return r, clock
}

// GoneOrUnverified is the fail-CLOSED reading: gone unless a fresh, successful answer says active.
// A cold cache, an unresolvable tenant and an unrecognised or empty state all read gone, where
// Deleted (fail-open) reads the first two active.
func TestGoneOrUnverifiedFailsClosedWhereDeletedFailsOpen(t *testing.T) {
	// Cold and failing: nothing has ever answered.
	down := &fixedLifecycle{err: errors.New("user-management unreachable")}
	r, _ := lifecycleOnClock(down.fetch)
	assert.True(t, r.GoneOrUnverified("acme"), "a cold tenant must read gone")
	quiesce(t, r)
	assert.Equal(t, 1, down.callCount())
	assert.True(t, r.GoneOrUnverified("acme"), "a tenant whose lookups fail must read gone")
	assert.False(t, r.Deleted("acme"), "premise: the fail-open gate reads that same tenant active")

	// A fresh active answer is the only thing that reads active.
	up := &fixedLifecycle{state: LifecycleActive}
	r, _ = lifecycleOnClock(up.fetch)
	assert.True(t, r.GoneOrUnverified("acme"), "before the first answer lands: gone")
	quiesce(t, r)
	assert.False(t, r.GoneOrUnverified("acme"), "a fresh active answer reads active")

	// A deleted tenant, and one in a state this reader cannot name, or an empty answer, read gone.
	for _, state := range []string{"purging", "some-future-state", ""} {
		f := &fixedLifecycle{state: state}
		r, _ = lifecycleOnClock(f.fetch)
		r.GoneOrUnverified("acme")
		quiesce(t, r)
		assert.Truef(t, r.GoneOrUnverified("acme"), "state %q reads gone", state)
	}
}

// A stale entry (past its TTL) and an entry whose latest refresh failed are not authoritative
// even though they hold a value that says active.
func TestGoneOrUnverifiedDistrustsAStaleOrFailedEntry(t *testing.T) {
	f := &fixedLifecycle{state: LifecycleActive}
	r, clock := lifecycleOnClock(f.fetch)

	r.GoneOrUnverified("acme")
	quiesce(t, r)
	require.False(t, r.GoneOrUnverified("acme"), "premise: a fresh active answer reads active")

	// Just inside the TTL: still authoritative, and no refresh was needed.
	clock.Advance(59 * time.Second)
	assert.False(t, r.GoneOrUnverified("acme"), "inside the TTL it is still fresh")
	quiesce(t, r)
	assert.Equal(t, 1, f.callCount())

	// Past the TTL the entry is stale: gone at once, whatever the refresh it triggers returns.
	f.mu.Lock()
	f.err = errors.New("down")
	f.mu.Unlock()
	clock.Advance(2 * time.Second)
	assert.True(t, r.GoneOrUnverified("acme"), "a stale entry must read gone")
	quiesce(t, r)
	assert.Equal(t, 2, f.callCount(), "the stale read triggered one refresh")

	// That refresh failed: the old value is kept, but it is no longer authoritative.
	assert.True(t, r.GoneOrUnverified("acme"), "an entry whose refresh failed must read gone")
	assert.False(t, r.Deleted("acme"), "premise: the fail-open gate still serves the last-known active")

	// Once the negative TTL passes and the authority answers again, it reads active again.
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	clock.Advance(2 * time.Minute)
	assert.True(t, r.GoneOrUnverified("acme"), "stale again: gone while the refresh is in flight")
	quiesce(t, r)
	assert.False(t, r.GoneOrUnverified("acme"), "a fresh active answer after the outage reads active")
}
