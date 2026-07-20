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
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond)) {
			backlogAdmitted++
		}
		// The interleave: a live post every ten backlog messages, each landing at
		// wall-clock now and so an hour ahead of the backlog timeline.
		if i%10 == 0 && gate("http", "acme", time.Time{}) {
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
		if gate("gw", "acme", base.Add(time.Duration(i)*time.Microsecond)) {
			admitted++
		}
		if i%10 == 0 {
			gate("http", "acme", time.Time{})
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
		if gate("gw", "acme", base.Add(time.Duration(i)*10*time.Millisecond)) {
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
		if gate("gw", "acme", fresh) {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, testBurst+2,
		"a caught-up capture consumer must meter as live traffic, not get its own ceiling")

	// And having done so, the live bucket is spent — an HTTP post from the same
	// tenant is shed rather than served from a second, untouched bucket.
	require.False(t, gate("http", "acme", time.Time{}),
		"live traffic must share one bucket across transports when nothing is lagging")
}

// Tenants are metered independently on both timelines; one tenant's backlog must
// not consume another's allowance.
func TestBacklogMeteringIsPerTenant(t *testing.T) {
	gate, _ := newTestGate()

	base := time.Now().Add(-time.Hour)
	for i := 0; i < 1000; i++ { // acme floods its own backlog bucket
		gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond))
	}

	admitted := 0
	for i := 0; i < 100; i++ { // globex sent a compliant backlog over the same window
		if gate("gw", "globex", base.Add(time.Duration(i)*10*time.Millisecond)) {
			admitted++
		}
	}
	require.Equal(t, 100, admitted, "one tenant's flood must not shed another's backlog")
}
