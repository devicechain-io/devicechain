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
func stallHarness(t *testing.T) (*captureHarness, *int) {
	t.Helper()
	shed := 0
	limits := core.StaticCeiling(platformRate, platformBurst)
	live := core.NewTenantRateLimiter(limits)
	gate := NewRateGate(live, core.NewTenantRateLimiter(limits), core.NewTenantRateLimiter(limits),
		core.NewTenantRateLimiter(limits), func(string, string) { shed++ })
	h := newCaptureHarnessWith(t, func(es *GatewayJetStreamSource) { es.meter = NewMeterAt(live) })
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

// THE YOUNG TAIL OF A STALL IS STILL THE STALL. A tenant sends its ceiling (1000/s) for 3 s and,
// 2.5 s into the stall, a 1000-message burst. On the send timeline that is compliant (the burst
// spends 1000 of the 2000 tokens). The messages younger than burst/rate (2 s) used to be metered
// at NOW on the live bucket: 2000 sustained + 1000 burst arriving together against 2000 tokens
// shed about 900 messages the broker had already PUBACKed. Once a tenant's reader is behind, the
// whole recovery is metered on the send timeline, not only the part older than the threshold.
func TestAStallTailWithAPermittedBurstIsNotShed(t *testing.T) {
	h, shed := stallHarness(t)

	const stall = 3 * time.Second
	first := time.Now().Add(-stall)
	seq := uint64(0)
	send := func(at time.Time) {
		seq++
		h.source.handle(capturedMsgAt(captureSubject, validEvent, seq, at, &recordingAck{}))
	}
	count := 0
	for i := 0; i < 3000; i++ {
		at := first.Add(time.Duration(i) * time.Millisecond)
		send(at)
		count++
		if i == 2500 { // the permitted burst, stamped at one instant
			for j := 0; j < 1000; j++ {
				send(at)
				count++
			}
		}
	}

	_, _, admitted, _ := h.counts()
	require.Zero(t, *shed, "a compliant burst inside a stall's young tail lost PUBACKed messages")
	require.Equal(t, count, admitted)
}

// The same, with the WHOLE burst (2000) landing in the last 100 ms before the reader catches up:
// no age cutoff can hand that to the idle live bucket without shedding, so the drain must stay on
// the send timeline while the tenant keeps sending.
func TestAStallTailWithAFullBurstJustBeforeCatchUpIsNotShed(t *testing.T) {
	h, shed := stallHarness(t)

	const stall = 3 * time.Second
	first := time.Now().Add(-stall)
	seq := uint64(0)
	send := func(at time.Time) {
		seq++
		h.source.handle(capturedMsgAt(captureSubject, validEvent, seq, at, &recordingAck{}))
	}
	count := 0
	for i := 0; i < 3000; i++ {
		at := first.Add(time.Duration(i) * time.Millisecond)
		send(at)
		count++
		if i == 2900 { // 100 ms before the end of the stall: a full permitted burst
			for j := 0; j < platformBurst-100; j++ { // the sustained traffic has spent 100 of it
				send(at)
				count++
			}
		}
	}

	_, _, admitted, _ := h.counts()
	require.Zero(t, *shed, "a compliant full burst at the end of a stall lost PUBACKed messages")
	require.Equal(t, count, admitted)
}

// Two readers share one MeterAt (the capture and the store source). A fresh message from one must
// not end the other's drain of the same tenant.
func TestAFreshMessageFromAnotherReaderDoesNotEndATenantsDrain(t *testing.T) {
	limits := core.StaticCeiling(platformRate, platformBurst)
	meter := NewMeterAt(core.NewTenantRateLimiter(limits))
	stalled := time.Now().Add(-3 * time.Second)
	require.Equal(t, stalled, meter("acme", stalled))
	_ = meter("acme", time.Now().Add(-time.Millisecond)) // the other reader, caught up
	young := time.Now().Add(-time.Second)
	require.Equal(t, young, meter("acme", young), "the drain was ended by another reader's fresh message")
}
