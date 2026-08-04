// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
)

const (
	testCeiling = 100.0
	testBurst   = 10
)

func newTestGate() (RateGate, *int) {
	shed := 0
	limits := func(string) (float64, int) { return testCeiling, testBurst }
	gate := NewRateGate(
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
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond), false) {
			backlogAdmitted++
		}
		// The interleave: a live post every ten backlog messages, each landing at
		// wall-clock now and so an hour ahead of the backlog timeline.
		if i%10 == 0 && gate("http", "acme", time.Time{}, false) {
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
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Microsecond), false) {
			admitted++
		}
		if i%10 == 0 {
			gate("http", "acme", time.Time{}, false)
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
		if gate("gw", "acme", base.Add(time.Duration(i)*10*time.Millisecond), false) {
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
		if gate("gw", "acme", fresh, false) {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, testBurst+2,
		"a caught-up capture consumer must meter as live traffic, not get its own ceiling")

	// And having done so, the live bucket is spent — an HTTP post from the same
	// tenant is shed rather than served from a second, untouched bucket.
	require.False(t, gate("http", "acme", time.Time{}, false),
		"live traffic must share one bucket across transports when nothing is lagging")
}

// Tenants are metered independently on both timelines; one tenant's backlog must
// not consume another's allowance.
func TestBacklogMeteringIsPerTenant(t *testing.T) {
	gate, _ := newTestGate()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 1000; i++ { // acme floods its own backlog bucket
		gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond), false)
	}

	admitted := 0
	for i := 0; i < 100; i++ { // globex sent a compliant backlog over the same window
		if gate("gw", "globex", base.Add(time.Duration(i)*10*time.Millisecond), false) {
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
		next := RateGate(func(string, string, time.Time, bool) bool { metered = true; return true })
		var refused []string
		gate := RefuseDeletedTenants(func(tenant string) bool { return tenant == "acme" }, next,
			func(_, tenant string) { refused = append(refused, tenant) })

		if gate("http", "acme", time.Time{}, false) {
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
			next := RateGate(func(string, string, time.Time, bool) bool { return allowed })
			gate := RefuseDeletedTenants(func(string) bool { return false }, next, nil)
			if got := gate("http", "acme", time.Time{}, false); got != allowed {
				t.Errorf("a live tenant must get the rate gate's own answer: got %v want %v", got, allowed)
			}
		}
	})

	// An unconfigured gate returns the underlying one untouched, so an instance with no
	// reachable user-management ingests exactly as before rather than refusing everything.
	t.Run("nil lifecycle gate leaves ingest alone", func(t *testing.T) {
		next := RateGate(func(string, string, time.Time, bool) bool { return true })
		if !RefuseDeletedTenants(nil, next, nil)("http", "acme", time.Time{}, false) {
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
		next := RateGate(func(_, _ string, _ time.Time, redelivery bool) bool {
			metered++
			return true
		})
		gate := RefuseDeletedTenants(func(tenant string) bool { return tenant == "acme" }, next, nil)

		if gate("gw", "acme", time.Time{}, true) {
			t.Error("a deleted tenant's REDELIVERY must be refused, not admitted because it already paid")
		}
		if metered != 0 {
			t.Error("a refused redelivery must not reach the meter at all")
		}
		if !gate("gw", "globex", time.Time{}, true) {
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
	limiter := core.NewTenantRateLimiter(func(string) (float64, int) { return 0, 1 })
	gate := NewRateGate(limiter, limiter, nil)

	for i := 0; i < 5; i++ {
		if !gate("gw", "acme", time.Time{}, true) {
			t.Fatalf("redelivery %d was shed; a redelivery must be admitted unmetered", i)
		}
	}
	if !gate("gw", "acme", time.Time{}, false) {
		t.Error("the redeliveries spent the tenant's only token, so the exemption is not real")
	}
	if gate("gw", "acme", time.Time{}, false) {
		t.Error("the limiter never sheds, so this test cannot show an exemption at all")
	}
}
