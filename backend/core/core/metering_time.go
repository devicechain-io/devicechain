// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MeteringClock names which time a dispatch was metered on: the trigger time its producer
// stamped, the broker's append time for the message that carried it, or now.
type MeteringClock string

const (
	// ClockStamped: the producer's stamped trigger time, which is the one meant to be used.
	ClockStamped MeteringClock = "stamped"
	// ClockAppend: no usable stamp, so the broker time the carrying message was stored at.
	ClockAppend MeteringClock = "append"
	// ClockNow: neither, so arrival — the limiter reads the zero time as now.
	ClockNow MeteringClock = "now"
)

// MeteringTime is the ONE reader of "which time is a dispatch metered on", shared by REACT's
// source-side outbound gate and the outbound-connectors egress limiter so the two ends of
// one outbound dimension meter the same time. Two implementations of the choice would drift,
// and a drift here is a backlog that passes one end and is shed at the other.
//
// It returns stamped when it is non-zero and not after appendTime — a thing cannot have been
// triggered after the message carrying it was stored, so a later stamp is a skewed or forged
// clock and is capped by falling back to appendTime. Otherwise appendTime when it is
// non-zero. Otherwise the zero time, which every TenantRateLimiter method reads as now.
//
// The second result says which clock was used, for NewRateClockFallbacks. A stamp later than
// appendTime counts as ClockAppend: it is a fallback, not the stamp.
//
// Neither input may come from device data: TenantRateLimiter.AllowAt bounds rewinds but not
// forgery, so a tenant-chosen time would let it mint tokens.
func MeteringTime(stamped, appendTime time.Time) (time.Time, MeteringClock) {
	if !stamped.IsZero() && (appendTime.IsZero() || !stamped.After(appendTime)) {
		return stamped, ClockStamped
	}
	if !appendTime.IsZero() {
		return appendTime, ClockAppend
	}
	return time.Time{}, ClockNow
}

// meteringFallbacks are the sources NewRateClockFallbacks counts: every clock but the stamp.
var meteringFallbacks = []MeteringClock{ClockAppend, ClockNow}

// NewRateClockFallbacks registers <area>_rate_clock_fallback_total{source} with source ∈
// {append, now}, both children created at 0 so an alert over it has a series to read before the
// first fallback, and returns a recorder that counts every non-stamped result of MeteringTime.
// A nil Microservice (unit tests) returns a recorder that counts nothing.
//
// Call it once per process, in the initialize phase, like every other collector: registering
// it twice panics.
//
// A sustained fallback means the producer has stopped stamping trigger times, so dispatches are
// metered on broker or arrival time and a catch-up can be shed as a flood again.
func NewRateClockFallbacks(ms *Microservice) func(MeteringClock) {
	if ms == nil {
		return func(MeteringClock) {}
	}
	vec := ms.NewCounterVec("rate_clock_fallback_total",
		"Count of dispatches metered on a fallback clock because they carried no usable trigger "+
			"time, by the clock used instead (append: the broker time the message was stored; "+
			"now: arrival)",
		[]string{"source"})
	children := make(map[MeteringClock]prometheus.Counter, len(meteringFallbacks))
	for _, c := range meteringFallbacks {
		children[c] = vec.WithLabelValues(string(c))
	}
	return func(c MeteringClock) {
		if ctr, ok := children[c]; ok {
			ctr.Inc()
		}
	}
}
