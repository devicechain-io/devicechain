// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	core "github.com/devicechain-io/dc-microservice/core"
)

// A PLATFORM STALL MUST NOT SHED A TENANT THAT WAS WITHIN ITS CEILING.
//
// The capture reader falls behind for a few seconds (a broker hiccup), then drains what queued.
// Each queued message is metered by its age. Before the threshold followed the tenant's burst,
// anything younger than a fixed 5 s was metered at NOW, so the whole stall arrived at the live
// bucket at once: the 2000-token burst absorbed 2 s of it and the rest was ack-dropped, messages
// the broker had already PUBACKed to the device, charged to a tenant that was sending exactly its
// ceiling. These tests drive the REAL gate and the REAL routing decision through the source's own
// handle, because the defect lives in which limiter a message is routed to, not in either bucket.

// stallHarness wires the capture source to the real gate over a limiter set at the platform
// default ceiling, and returns how many messages of a stalled run were admitted and shed.
func stallHarness(t *testing.T, opts ...GateOption) (*captureHarness, *int) {
	t.Helper()
	shed := 0
	limits := core.StaticCeiling(platformRate, platformBurst)
	gate := NewRateGate(core.NewTenantRateLimiter(limits), core.NewTenantRateLimiter(limits), core.NewTenantRateLimiter(limits),
		core.NewTenantRateLimiter(limits), func(string, string) { shed++ }, opts...)
	h := newCaptureHarnessWith(t, func(es *GatewayJetStreamSource) { es.meter = NewMeterAt() })
	h.gate = gate
	return h, &shed
}

// replayStall sends `count` messages `spacing` apart, the last of them `stall` ago, and feeds
// them to the source in one go: the shape of a queue that built up behind a stalled reader and
// is released at once.
func replayStall(t *testing.T, h *captureHarness, count int, spacing, stall time.Duration) {
	t.Helper()
	first := time.Now().Add(-stall)
	for i := 0; i < count; i++ {
		h.source.handle(capturedMsgAt(captureSubject, validEvent, uint64(i+1),
			first.Add(time.Duration(i)*spacing), &recordingAck{}))
	}
}

// The case from the field: a tenant at exactly its ceiling (1000/s), a 3 s backlog, nothing shed.
// 3 s sits in the old dead zone: past the 2 s the burst covers, short of the fixed 5 s.
func TestAPlatformStallDoesNotShedATenantAtItsCeiling(t *testing.T) {
	h, shed := stallHarness(t)

	// 3 s of traffic at the ceiling, queued and released together.
	const count = 3000
	replayStall(t, h, count, time.Millisecond, 3*time.Second)

	_, _, admitted, _ := h.counts()
	require.Zero(t, *shed, "a tenant sending at its ceiling lost PUBACKed messages to a platform stall")
	require.Equal(t, count, admitted)
}

// The same, across the whole old dead zone and both sides of it: every stall from under the
// burst's cover to well past the fixed threshold, for a tenant at its ceiling.
func TestNoStallLengthShedsATenantAtItsCeiling(t *testing.T) {
	for _, stall := range []time.Duration{time.Second, 2500 * time.Millisecond, 3 * time.Second,
		4500 * time.Millisecond, 5500 * time.Millisecond, 20 * time.Second} {
		h, shed := stallHarness(t)
		count := int(stall / time.Millisecond)
		replayStall(t, h, count, time.Millisecond, stall)

		_, _, admitted, _ := h.counts()
		require.Zerof(t, *shed, "stall %v shed a tenant at its ceiling", stall)
		require.Equalf(t, count, admitted, "stall %v", stall)
	}
}

// THE COUNTERWEIGHT. Routing the stall to the send timeline must not become a way to send over
// the ceiling: a tenant genuinely at 3x its ceiling is still shed, and by the volume its own
// timestamps say it exceeded, not wholesale and not nothing.
func TestAPlatformStallStillShedsATenantAboveItsCeiling(t *testing.T) {
	h, shed := stallHarness(t)

	// 3x the ceiling for 3 s: one message every 333 microseconds, 9000 in all.
	const count = 9000
	replayStall(t, h, count, time.Second/3000, 3*time.Second)

	_, _, admitted, _ := h.counts()
	require.Equal(t, count, admitted+*shed, "every message is admitted or counted shed")
	// Admitted is bounded by what two timelines can grant: the older part of the backlog on its
	// send timeline (burst + rate over the span it covers) plus the burst of the live bucket.
	// Far below the 9000 sent, which is the point; the exact figure is the limiter's.
	const bound = 2*platformBurst + int(platformRate*3) // 7000
	require.LessOrEqual(t, admitted, bound, "an over-ceiling tenant must not be admitted wholesale")
	require.Greater(t, *shed, count/5, "an over-ceiling tenant was barely shed")
}

// stallWithBurst replays a tenant sending its ceiling (1000/s) through a stall of `stall`, plus a
// permitted burst of `burst` messages stamped `burstAgo` before the reader catches up. On the
// send timeline that is compliant, so nothing the broker PUBACKed may be shed, whatever the
// stall's length and wherever in it the burst fell.
func stallWithBurst(t *testing.T, stall time.Duration, burst int, burstAgo time.Duration) {
	t.Helper()
	h, shed := stallHarness(t)
	first := time.Now().Add(-stall)
	burstAt := int((stall - burstAgo) / time.Millisecond)
	seq := uint64(0)
	send := func(at time.Time) {
		seq++
		h.source.handle(capturedMsgAt(captureSubject, validEvent, seq, at, &recordingAck{}))
	}
	count := 0
	for i := 0; i < int(stall/time.Millisecond); i++ {
		at := first.Add(time.Duration(i) * time.Millisecond)
		send(at)
		count++
		if i == burstAt {
			for j := 0; j < burst; j++ {
				send(at)
				count++
			}
		}
	}
	_, _, admitted, _ := h.counts()
	require.Zerof(t, *shed, "stall %v, burst %d at %v ago: a compliant tenant lost PUBACKed messages", stall, burst, burstAgo)
	require.Equal(t, count, admitted)
}

// The audit's case: a 3 s stall and a permitted 1000-message burst 500 ms before catch-up.
func TestAStallTailWithAPermittedBurstIsNotShed(t *testing.T) {
	stallWithBurst(t, 3*time.Second, 1000, 500*time.Millisecond)
}

// A stall SHORTER than the burst's cover (1.9 s of the 2 s) used to be metered at now on the idle
// live bucket and shed a compliant burst (864 were shed). Stall length is irrelevant now.
func TestAStallShorterThanTheBurstCoverWithAPermittedBurstIsNotShed(t *testing.T) {
	stallWithBurst(t, 1900*time.Millisecond, 1000, 400*time.Millisecond)
}

// The whole burst inside the last 100 ms: no age cutoff could hand it to a fresh bucket.
func TestAStallTailWithAFullBurstJustBeforeCatchUpIsNotShed(t *testing.T) {
	stallWithBurst(t, 3*time.Second, platformBurst-100, 100*time.Millisecond)
}

// A long stall and a burst.
func TestALongStallWithAPermittedBurstIsNotShed(t *testing.T) {
	stallWithBurst(t, 8*time.Second, 1500, 3*time.Second)
}

// shedGate builds the gate the way main wires it, with a contention shed that prices a fresh
// message at `units` tokens, over the platform-default ceiling.
func shedGate(units int) (RateGate, *int) {
	shed := 0
	limits := core.StaticCeiling(platformRate, platformBurst)
	inner := NewRateGate(core.NewTenantRateLimiter(limits), core.NewTenantRateLimiter(limits),
		core.NewTenantRateLimiter(limits), core.NewTenantRateLimiter(limits),
		func(string, string) { shed++ }, WithShedUnits(func(string) int { return units }))
	meter := NewMeterAt()
	return func(source, tenant string, sentAt time.Time, redelivery bool, origin Origin) bool {
		return inner(source, tenant, meter(tenant, sentAt), redelivery, origin)
	}, &shed
}

// THE CONTENTION SHED STILL REACHES FRESH TRAFFIC OF A TENANT THAT HAS BEEN BEHIND. One stale
// message used to switch a continuously-sending tenant onto an unshed allowance for as long as it
// kept sending (a probe: with the hard-drop band on, 1 stale + 500 fresh admitted all 501; main
// sheds the 500). Fresh traffic is shed whatever came before it.
func TestContentionShedsFreshTrafficAfterALag(t *testing.T) {
	gate, _ := shedGate(maxShedUnits) // the hard drop
	require.True(t, gate("gw", "acme", time.Now().Add(-3*time.Second), false, OriginAuthenticated),
		"a stale message is a drained backlog: never shed by contention")
	admitted := 0
	for i := 0; i < 500; i++ {
		if gate("gw", "acme", time.Now().Add(-time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}
	require.Zero(t, admitted, "fresh traffic must be shed under the hard drop, as on main")
}

// A throttle (a factor of one half costs two tokens) admits about half a burst of fresh traffic.
func TestContentionThrottlesFreshTrafficByItsFactor(t *testing.T) {
	gate, _ := shedGate(2)
	admitted := 0
	for i := 0; i < platformBurst; i++ {
		if gate("gw", "acme", time.Now().Add(-time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}
	require.InDelta(t, platformBurst/2, admitted, 150)
}

// A drained backlog is durably accepted data: contention never sheds it, however hard the drop.
func TestContentionNeverShedsADrainedBacklog(t *testing.T) {
	gate, shed := shedGate(maxShedUnits)
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 1000; i++ { // exactly the ceiling on the send timeline
		require.True(t, gate("gw", "acme", base.Add(time.Duration(i)*time.Millisecond), false, OriginAuthenticated))
	}
	require.Zero(t, *shed)
}

// Backlog and fresh traffic are charged to ONE bucket, so a stalled tenant is not granted a second
// allowance for its fresh tail.
func TestNoContentionLeavesFreshTrafficOnTheOrdinaryCeiling(t *testing.T) {
	gate, _ := shedGate(1)
	admitted := 0
	for i := 0; i < 3*platformBurst; i++ {
		if gate("gw", "acme", time.Now().Add(-time.Millisecond), false, OriginAuthenticated) {
			admitted++
		}
	}
	// Refill during the loop (slow under -race) is at most a few hundred; a doubled ceiling is not.
	require.LessOrEqual(t, admitted, platformBurst+300)
	require.GreaterOrEqual(t, admitted, platformBurst-5)
}

// A broker clock AHEAD of this pod (the append time is the stream leader's wall clock) must be
// harmless. Uncapped, the first message moves the bucket's clock an hour into the future and every
// honest message after it is charged at that mark, accruing nothing: the tenant is locked out.
func TestABrokerClockAheadOfThePodIsHarmless(t *testing.T) {
	gate, _ := newTestGate()
	ahead := time.Now().Add(time.Hour)
	admitted := 0
	for i := 0; i < 3*testBurst; i++ {
		if gate("gw", "acme", ahead, false, OriginAuthenticated) {
			admitted++
		}
	}
	require.LessOrEqual(t, admitted, testBurst+2, "a clock ahead must not mint tokens")
	time.Sleep(200 * time.Millisecond)
	require.True(t, gate("gw", "acme", time.Now(), false, OriginAuthenticated),
		"a broker clock ahead locked the tenant out for the length of the lead")
}
