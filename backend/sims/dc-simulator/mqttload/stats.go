// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"math"
	"sync/atomic"
	"time"
)

// Counters are a fleet's running accounting. Every field is an atomic: sessions update them
// from their own goroutines and the reporter reads them from another.
type Counters struct {
	// Sent is publishes handed to the transport (QoS 1 or 0).
	Sent atomic.Int64
	// Acked is QoS 1 publishes whose PUBACK the session store saw.
	Acked atomic.Int64
	// Written is QoS 0 publishes the client wrote to the socket (there is no acknowledgement
	// to count).
	Written atomic.Int64
	// Unconfirmed is publishes that ended with no PUBACK: the token failed, the connection
	// was lost under it, or AckTimeout passed. The event may still have been stored; the
	// reconciliation treats it as ambiguous.
	Unconfirmed atomic.Int64
	// Resent is QoS 1 publishes a resumed session re-sent (their token closed with no error
	// and no PUBACK). However each one ended, the broker may have received it twice, so a
	// stored duplicate of it can stand in for a lost event in a count: it widens the count
	// mode's masked-loss bound exactly as an unconfirmed publish does.
	Resent atomic.Int64
	// NotSentDisconnected is ticks a session skipped because its connection was down, and
	// SkippedInflight ticks skipped because MaxInflight publishes were already waiting.
	// Neither was sent, so neither can be stored.
	NotSentDisconnected atomic.Int64
	SkippedInflight     atomic.Int64
	// Overruns is ticks a session ran late by more than a whole interval; the missed ticks
	// are dropped rather than burst, so a slow client shows as a shortfall, not as a spike.
	Overruns atomic.Int64

	ConnectAttempts  atomic.Int64
	ConnectFailures  atomic.Int64
	Connects         atomic.Int64
	ConnectionLosses atomic.Int64
	// TLSConns and PlainConns are connections OBSERVED on the dialled socket, whatever the
	// flag said.
	TLSConns   atomic.Int64
	PlainConns atomic.Int64

	Inflight atomic.Int64 // gauge: publishes waiting for an acknowledgement
	Up       atomic.Int64 // gauge: sessions currently connected
}

// Snapshot is a point-in-time read of Counters, for the report.
type Snapshot struct {
	Sent                int64 `json:"sent"`
	Acked               int64 `json:"pubacked"`
	Written             int64 `json:"writtenQoS0"`
	Unconfirmed         int64 `json:"unconfirmed"`
	Resent              int64 `json:"resent"`
	NotSentDisconnected int64 `json:"notSentDisconnected"`
	SkippedInflight     int64 `json:"skippedInflight"`
	Overruns            int64 `json:"overruns"`
	ConnectAttempts     int64 `json:"connectAttempts"`
	ConnectFailures     int64 `json:"connectFailures"`
	Connects            int64 `json:"connects"`
	ConnectionLosses    int64 `json:"connectionLosses"`
	TLSConns            int64 `json:"tlsConnections"`
	PlainConns          int64 `json:"plaintextConnections"`
	Inflight            int64 `json:"inflight"`
	Up                  int64 `json:"sessionsUp"`
}

// Snapshot reads the counters.
func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		Sent: c.Sent.Load(), Acked: c.Acked.Load(), Written: c.Written.Load(),
		Unconfirmed: c.Unconfirmed.Load(), Resent: c.Resent.Load(), NotSentDisconnected: c.NotSentDisconnected.Load(),
		SkippedInflight: c.SkippedInflight.Load(), Overruns: c.Overruns.Load(),
		ConnectAttempts: c.ConnectAttempts.Load(), ConnectFailures: c.ConnectFailures.Load(),
		Connects: c.Connects.Load(), ConnectionLosses: c.ConnectionLosses.Load(),
		TLSConns: c.TLSConns.Load(), PlainConns: c.PlainConns.Load(),
		Inflight: c.Inflight.Load(), Up: c.Up.Load(),
	}
}

// latBuckets: 8 buckets per octave from 1 microsecond to 2^40 microseconds (about 12.7 days),
// so any percentile is within ~9% of the true value at a fixed few KB per histogram.
const (
	latPerOctave = 8
	latOctaves   = 40
	latBuckets   = latPerOctave * latOctaves
)

// LatencyHist is a lock-free log-scale histogram of PUBACK latencies. It keeps no samples, so
// a 60 minute run at thousands of publishes a second costs the same few kilobytes as a short
// one.
type LatencyHist struct {
	counts [latBuckets]atomic.Int64
	n      atomic.Int64
	max    atomic.Int64 // nanoseconds
}

func latBucket(d time.Duration) int {
	us := float64(d) / float64(time.Microsecond)
	if us < 1 {
		return 0
	}
	b := int(math.Log2(us) * latPerOctave)
	if b >= latBuckets {
		return latBuckets - 1
	}
	return b
}

// bucketUpper is the upper bound of bucket b, the value a percentile landing in it reports
// (never an underestimate).
func bucketUpper(b int) time.Duration {
	return time.Duration(math.Exp2(float64(b+1)/latPerOctave) * float64(time.Microsecond))
}

// Observe records one latency.
func (h *LatencyHist) Observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.counts[latBucket(d)].Add(1)
	h.n.Add(1)
	for {
		cur := h.max.Load()
		if int64(d) <= cur || h.max.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

// Percentiles are latencies in milliseconds.
type Percentiles struct {
	Count int64   `json:"count"`
	P50   float64 `json:"p50Ms"`
	P90   float64 `json:"p90Ms"`
	P99   float64 `json:"p99Ms"`
	P999  float64 `json:"p999Ms"`
	Max   float64 `json:"maxMs"`
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Percentiles reads p50/p90/p99/p99.9 and the exact maximum.
func (h *LatencyHist) Percentiles() Percentiles {
	n := h.n.Load()
	mx := time.Duration(h.max.Load())
	p := Percentiles{Count: n, Max: ms(mx)}
	if n == 0 {
		return p
	}
	targets := []struct {
		q   float64
		dst *float64
	}{{0.50, &p.P50}, {0.90, &p.P90}, {0.99, &p.P99}, {0.999, &p.P999}}
	var seen int64
	ti := 0
	for b := 0; b < latBuckets && ti < len(targets); b++ {
		seen += h.counts[b].Load()
		for ti < len(targets) && float64(seen) >= targets[ti].q*float64(n) {
			v := bucketUpper(b)
			if v > mx {
				v = mx // a bucket's upper bound can exceed the largest sample
			}
			*targets[ti].dst = ms(v)
			ti++
		}
	}
	return p
}

// Interval is one reporting slice of a run: what happened in the last report interval.
type Interval struct {
	AtSeconds   float64 `json:"atSeconds"`
	Sent        int64   `json:"sent"`
	Acked       int64   `json:"pubacked"`
	Unconfirmed int64   `json:"unconfirmed"`
	NotSent     int64   `json:"notSent"`
	Up          int64   `json:"sessionsUp"`
	Inflight    int64   `json:"inflight"`
	Losses      int64   `json:"connectionLosses"`
	P99Ms       float64 `json:"ackP99Ms"`
}
