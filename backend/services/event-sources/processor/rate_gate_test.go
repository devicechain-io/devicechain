// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
)

const (
	testCeiling = 100.0
	testBurst   = 10
)

func newTestGate() (RateGate, *int) {
	shed := 0
	limits := core.StaticCeiling(testCeiling, testBurst)
	gate := NewRateGate(
		core.NewTenantRateLimiter(limits),
		core.NewTenantRateLimiter(limits),
		core.NewTenantRateLimiter(limits),
		func(string, string) { shed++ })
	return gate, &shed
}

// THE MINTING BYPASS — the defect this gate's two-limiter shape exists to close.
//
// The single-bucket design that preceded it fed one token bucket both wall-clock
// arrivals and old send times. A bucket accrues from the last timestamp it saw, so
// every jump forward to now re-accrued from an hours-stale mark and refilled to
// burst, which the following rewind then spent. A tenant could therefore pace live
// HTTP posts against their own draining backlog and mint ~burst admissions per
// interleave — an unbounded bypass that scaled with consumer lag, where one second
// of lag already turned a 100/s ceiling into roughly 2000 admissions.
//
// The attack, run against the real gate: drain a 10x-over-ceiling backlog while
// dripping one live message in every ten. If the bypass is open the backlog is
// admitted essentially in full.
func TestPacingLiveTrafficAgainstADrainCannotMintAdmissions(t *testing.T) {
	gate, _ := newTestGate()

	// A backlog sent an hour ago at 10x the ceiling.
	base := time.Now().Add(-time.Hour)
	backlogAdmitted, liveAdmitted := 0, 0
	for i := 0; i < 1000; i++ {
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond), false, OriginAuthenticated) {
			backlogAdmitted++
		}
		// The interleave: a live message every ten backlog messages, each landing at
		// wall-clock now and so an hour ahead of the backlog timeline. It is
		// authenticated live traffic (an external-broker message, sent at now), the kind
		// that shares a tenant's live allowance; HTTP has an allowance of its own and
		// could not show the minting at all.
		if i%10 == 0 && gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated) {
			liveAdmitted++
		}
	}

	// The backlog is metered on its own clock, so it is shed to what the ceiling
	// permits over the window it was sent in — the interleaved live traffic buys it
	// nothing. On the single-bucket design this admitted 900+.
	require.InDelta(t, 109, backlogAdmitted, 2,
		"live traffic interleaved with a drain must not mint backlog admissions")
	// And the live drip gets only its own bucket's burst, not a token per post.
	require.LessOrEqual(t, liveAdmitted, testBurst+2,
		"the live bucket must not be refilled by the backlog rewinding underneath it")
}

// The same bypass, at the lag that makes it dangerous. It never needed an outage:
// on the single-bucket design one second of ordinary consumer lag was enough,
// which is well within normal operation under load.
func TestASecondOfConsumerLagIsNotABypass(t *testing.T) {
	gate, _ := newTestGate()

	base := time.Now().Add(-time.Second)
	admitted := 0
	for i := 0; i < 1000; i++ {
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Microsecond), false, OriginAuthenticated) {
			admitted++
		}
		if i%10 == 0 {
			gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated)
		}
	}

	require.Less(t, admitted, 100,
		"one second of lag must not admit a 10x flood; it did on the single-bucket design")
}

// The I4 property still holds THROUGH the gate, not merely through the limiter:
// a compliant backlog is admitted in full.
func TestGateAdmitsACompliantBacklogInFull(t *testing.T) {
	gate, shed := newTestGate()

	base := time.Now().Add(-time.Hour)
	admitted := 0
	for i := 0; i < 1000; i++ {
		if gate("gw", "acme", base.Add(time.Duration(i)*10*time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}

	require.Equal(t, 1000, admitted, "a compliant recovered backlog must not be shed")
	require.Zero(t, *shed, "nothing should have been reported shed")
}

// Steady state routes to the LIVE limiter, so the ceiling is enforced exactly and
// the backlog limiter never engages. Without this, a caught-up consumer would meter
// on a second bucket and every tenant would quietly get twice their ceiling.
func TestCaughtUpTrafficIsMeteredOnTheLiveLimiter(t *testing.T) {
	gate, _ := newTestGate()

	// Fresh captured messages: below the backlog threshold, so they are live.
	fresh := time.Now().Add(-BacklogThreshold / 2)
	admitted := 0
	for i := 0; i < 500; i++ {
		if gate("gw", "acme", fresh, false, OriginAuthenticated) {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, testBurst+2,
		"a caught-up capture consumer must meter as live traffic, not get its own ceiling")

	// And having done so, the live bucket is spent — an external-broker message from
	// the same tenant is shed rather than served from a second, untouched bucket.
	require.False(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated),
		"authenticated live traffic must share one bucket across transports when nothing is lagging")
}

// HTTP names its tenant before any credential is checked, so an HTTP flood naming a
// tenant must not spend the allowance its authenticated traffic spends: shedding that
// traffic ack-drops captured messages the broker already PUBACKed. And the other way
// round, the tenant's own devices exhausting their allowance must not shut HTTP out.
func TestAnHTTPFloodCannotSpendAuthenticatedAllowance(t *testing.T) {
	gate, _ := newTestGate()

	// Spend acme's HTTP allowance until it sheds.
	for i := 0; i < 500 && gate("http", "acme", time.Time{}, false, OriginUntrusted); i++ {
	}
	require.False(t, gate("http", "acme", time.Time{}, false, OriginUntrusted),
		"the HTTP allowance must be spent before this test means anything")

	// Every authenticated shape is still admitted: a caught-up capture message, an
	// external-broker message (zero send time), a backlog message.
	assert.True(t, gate("gw", "acme", time.Now(), false, OriginAuthenticated),
		"an HTTP flood shed acme's captured telemetry")
	assert.True(t, gate("mqtt", "acme", time.Time{}, false, OriginAuthenticated),
		"an HTTP flood shed acme's external-broker telemetry")
	assert.True(t, gate("gw", "acme", time.Now().Add(-time.Hour), false, OriginAuthenticated),
		"an HTTP flood shed acme's capture backlog")

	// Mirror: a fresh gate whose live allowance authenticated traffic has spent.
	gate, _ = newTestGate()
	for i := 0; i < 500 && gate("mqtt", "globex", time.Time{}, false, OriginAuthenticated); i++ {
	}
	require.False(t, gate("mqtt", "globex", time.Time{}, false, OriginAuthenticated),
		"the live allowance must be spent before this half means anything")
	assert.True(t, gate("http", "globex", time.Time{}, false, OriginUntrusted),
		"a tenant's own devices spending their allowance must not shut its HTTP ingest out")
}

// A gate missing an allowance cannot meter what would be routed to it; building one
// fails at construction rather than at the first message.
func TestARateGateNeedsAllThreeLimiters(t *testing.T) {
	l := core.NewTenantRateLimiter(core.StaticCeiling(1, 1))
	for name, args := range map[string][3]*core.TenantRateLimiter{
		"live": {nil, l, l}, "backlog": {l, nil, l}, "untrusted": {l, l, nil},
	} {
		assert.Panicsf(t, func() { NewRateGate(args[0], args[1], args[2], nil) }, "missing %s limiter", name)
	}
}

// A broker's append time is stamped by the stream leader's wall clock, so it is not
// monotonic: a leader change between servers whose clocks disagree steps it backwards.
// Fed straight to the token bucket, the first stepped message rewinds the bucket's clock
// and everything after re-accrues from the older point, so the tenant gets the stepped
// span's worth of tokens a second time. Under the limiter's mark the older times are
// charged at the latest time the bucket has seen, so the step costs over-shedding and
// admits nothing extra.
func TestBacklogLimiterSurvivesABackwardsAppendTime(t *testing.T) {
	gate, _ := newTestGate()

	base := time.Now().Add(-time.Hour) // well past BacklogThreshold: the backlog limiter
	admitted := 0
	for i := 0; i < 100; i++ { // 1 s at exactly the ceiling: compliant
		if gate("gw", "acme", base.Add(time.Duration(i)*10*time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}
	require.Equal(t, 100, admitted, "the compliant second must be admitted in full")

	stepped := base.Add(-9 * time.Second) // a leader change: 10 s behind the last append
	for i := 0; i < 1000; i++ {           // then 1 s at 10x the ceiling
		if gate("gw", "acme", stepped.Add(time.Duration(i)*time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}

	// The bucket's clock never leaves base .. base+1 s, so across both segments it admits
	// at most one burst plus one second at the ceiling. Rewound, the stepped flood gets a
	// second of its own on top: ~210.
	require.LessOrEqual(t, admitted, testBurst+int(testCeiling)*1+1,
		"a backwards append time must not re-accrue the ceiling")
}

// Tenants are metered independently on both timelines; one tenant's backlog must
// not consume another's allowance.
func TestBacklogMeteringIsPerTenant(t *testing.T) {
	gate, _ := newTestGate()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 1000; i++ { // acme floods its own backlog bucket
		gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond), false, OriginAuthenticated)
	}

	admitted := 0
	for i := 0; i < 100; i++ { // globex sent a compliant backlog over the same window
		if gate("gw", "globex", base.Add(time.Duration(i)*10*time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}
	require.Equal(t, 100, admitted, "one tenant's flood must not shed another's backlog")
}

// 🔴 ADR-077 at the ingest admission hook — the gate that covers the transports nothing
// else does.
//
// Composed onto the rate gate rather than added per source, because the three inbound
// sources (HTTP, external MQTT, capture stream) each call exactly one admission hook and
// a fourth transport must not be able to arrive with the ceiling wired and the lifecycle
// check missing.
func TestRefuseDeletedTenants(t *testing.T) {
	t.Run("a deleted tenant is refused before the rate gate is consulted", func(t *testing.T) {
		metered := false
		next := RateGate(func(string, string, time.Time, bool, Origin) bool { metered = true; return true })
		var refused []string
		gate := RefuseDeletedTenants(func(tenant string) bool { return tenant == "acme" }, next,
			func(_, tenant string) { refused = append(refused, tenant) })

		if gate("http", "acme", time.Time{}, false, OriginUntrusted) {
			t.Fatal("a deleted tenant's message must not be admitted")
		}
		if metered {
			t.Error("a refused message must not spend the tenant's rate budget")
		}
		if len(refused) != 1 || refused[0] != "acme" {
			t.Errorf("the refusal must be accounted apart from a rate shed, got %v", refused)
		}
	})

	// The negative control: a live tenant still reaches the rate gate and is answered by
	// it. Without this, a wrapper that refused everything would satisfy the case above
	// and silently stop all ingest on the instance.
	t.Run("a live tenant is passed through to the rate gate", func(t *testing.T) {
		for _, allowed := range []bool{true, false} {
			next := RateGate(func(string, string, time.Time, bool, Origin) bool { return allowed })
			gate := RefuseDeletedTenants(func(string) bool { return false }, next, nil)
			if got := gate("http", "acme", time.Time{}, false, OriginUntrusted); got != allowed {
				t.Errorf("a live tenant must get the rate gate's own answer: got %v want %v", got, allowed)
			}
		}
	})

	// An unconfigured gate returns the underlying one untouched, so an instance with no
	// reachable user-management ingests exactly as before rather than refusing everything.
	t.Run("nil lifecycle gate leaves ingest alone", func(t *testing.T) {
		next := RateGate(func(string, string, time.Time, bool, Origin) bool { return true })
		if !RefuseDeletedTenants(nil, next, nil)("http", "acme", time.Time{}, false, OriginUntrusted) {
			t.Error("an unwired lifecycle gate must not refuse ingest")
		}
	})

	// 🔴 THE REGRESSION THAT MADE redelivery A PARAMETER. The capture source used to
	// skip the whole gate on a redelivery, to avoid metering a message that had already
	// paid. Once the lifecycle refusal was composed in front of the meter, that skip
	// silently stopped refusing a deleted tenant's redeliveries too — so a tenant
	// deleted between delivery 1 and a retry had that retry admitted, into a purge that
	// was already sweeping.
	//
	// Both halves are asserted here, because either alone passes with the bug present:
	// checking only the refusal passes with metering also unexempted, and checking only
	// the exemption is what the original code did.
	t.Run("a redelivery is exempt from metering and not from the lifecycle refusal", func(t *testing.T) {
		metered := 0
		next := RateGate(func(_, _ string, _ time.Time, redelivery bool, _ Origin) bool {
			metered++
			return true
		})
		gate := RefuseDeletedTenants(func(tenant string) bool { return tenant == "acme" }, next, nil)

		if gate("gw", "acme", time.Time{}, true, OriginAuthenticated) {
			t.Error("a deleted tenant's REDELIVERY must be refused, not admitted because it already paid")
		}
		if metered != 0 {
			t.Error("a refused redelivery must not reach the meter at all")
		}
		if !gate("gw", "globex", time.Time{}, true, OriginAuthenticated) {
			t.Fatal("a live tenant's redelivery must still be admitted")
		}
		if metered != 1 {
			t.Errorf("a live tenant's redelivery must reach the meter, which exempts it there; "+
				"got %d meter calls", metered)
		}
	})
}

// TestARedeliveryIsNotMetered pins the exemption in the layer that owns it, which is
// where it moved to. NewRateGate must admit a redelivery without touching the bucket:
// asserting through a REAL limiter rather than a stub, because a stub cannot show that
// the token was never spent.
func TestARedeliveryIsNotMetered(t *testing.T) {
	// A ceiling of one token and no refill, so a single metered call exhausts it.
	limiter := core.NewTenantRateLimiter(core.StaticCeiling(0, 1))
	// The same limiter in every position, so a redelivery that reached ANY of them
	// would spend the one token.
	gate := NewRateGate(limiter, limiter, limiter, nil)

	for i := 0; i < 5; i++ {
		if !gate("gw", "acme", time.Time{}, true, OriginAuthenticated) {
			t.Fatalf("redelivery %d was shed; a redelivery must be admitted unmetered", i)
		}
	}
	if !gate("gw", "acme", time.Time{}, false, OriginAuthenticated) {
		t.Error("the redeliveries spent the tenant's only token, so the exemption is not real")
	}
	if gate("gw", "acme", time.Time{}, false, OriginAuthenticated) {
		t.Error("the limiter never sheds, so this test cannot show an exemption at all")
	}
}
