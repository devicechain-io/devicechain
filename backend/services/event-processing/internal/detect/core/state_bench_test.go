// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// What one message costs the single DETECT goroutine every tenant shares.
//
// 🔴 THE TWO AXES ARE INDEPENDENT, AND A BENCHMARK THAT CONFLATES THEM CANNOT SEE THE
// DEFECT. They are:
//
//   - k, the entries in one message — bounded by the ingest ceiling
//     (config.DefaultMaxReadingsPerMessage on the JSON transports);
//   - W, the window DEPTH the message lands in — bounded by NOTHING. A 24h sliding rule
//     on a 1 Hz series holds W ≈ 86 400 samples, and this engine's own state-budget note
//     expects "hundreds of thousands of retained samples".
//
// An earlier version of this file generated one shuffled batch and folded it into an EMPTY
// window, which makes W ≡ k and reports a curve that only holds when the window is as
// shallow as the message. The question that matters is what a COMPLIANT, at-the-ceiling
// message costs when it arrives at a DEEP window — so W is pre-filled outside the timer and
// only k is delivered inside it.
const benchWindow = 24 * time.Hour

// benchDeepWindow pre-fills one series with W in-order samples, then times the delivery of
// ONE message of k samples whose event times are shuffled inside the span already held — a
// store-and-forward device handing over a buffered run. The threshold is unreachable so
// nothing is emitted: the measurement is window maintenance, not the emit path.
func benchDeepWindow(b *testing.B, agg AggOp, w, k int) {
	base := time.Unix(0, 0)
	rule := Rule{ID: "r", Kind: SlidingAgg, Agg: agg, Window: benchWindow, Op: GT, Thresh: 1e18}
	key := SeriesKey{Rule: "r", Series: "d"}

	// The late batch: k times drawn from the middle of the pre-filled span, shuffled, so
	// every one of them lands mid-buffer rather than appending.
	late := make([]sample, k)
	r := rand.New(rand.NewSource(42))
	for i := range late {
		off := w/4 + r.Intn(w/2)
		late[i] = sample{t: base.Add(time.Duration(off) * time.Millisecond), v: float64(off % 977)}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		e := NewEngine([]Rule{rule}, time.Hour)
		for j := 0; j < w; j++ {
			e.ProcessEvent(Event{Seq: uint64(j + 1), Key: key, Time: base.Add(time.Duration(j) * time.Millisecond),
				Value: float64(j % 977), Match: true})
		}
		e.Drain()
		b.StartTimer()

		for j, x := range late {
			e.ProcessEvent(Event{Seq: uint64(w + j + 1), Key: key, Time: x.t, Value: x.v, Match: true})
		}
	}
}

// The headline curve: ONE message at the 1000-entry ceiling, against window depths from a
// shallow rule to a 24h 1 Hz series and beyond. The cost must not grow with W.
func BenchmarkCappedMessageDeepWindow(b *testing.B) {
	for _, w := range []int{1000, 16000, 86400, 200000} {
		b.Run(fmt.Sprintf("W=%d/min", w), func(b *testing.B) { benchDeepWindow(b, AggMin, w, 1000) })
		b.Run(fmt.Sprintf("W=%d/sum", w), func(b *testing.B) { benchDeepWindow(b, AggSum, w, 1000) })
	}
}

// The other axis held fixed: batch size against a FIXED deep window, which is what says
// whether the per-message cost is linear in k or quadratic in it.
func BenchmarkBatchSizeFixedWindow(b *testing.B) {
	for _, k := range []int{125, 250, 500, 1000, 2000} {
		b.Run(fmt.Sprintf("k=%d", k), func(b *testing.B) { benchDeepWindow(b, AggMin, 86400, k) })
	}
}

// The degenerate shape the previous benchmark measured, kept ONLY so the two stay
// comparable and the difference between them remains visible: W and k rise together.
func BenchmarkShallowWindowBatch(b *testing.B) {
	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) { benchDeepWindow(b, AggMin, n, n) })
	}
}
