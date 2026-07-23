// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEpochSourceMonotoneAcrossClockStepBack is the core invariant: even when the
// wall clock jumps backwards between mints, every epoch is strictly greater than the
// last — otherwise a re-derived presence transition could carry an epoch the
// projection already superseded and be silently rejected (ADR-067).
func TestEpochSourceMonotoneAcrossClockStepBack(t *testing.T) {
	// A clock that steps: forward, then a big step BACK, then a tiny forward. The
	// floor must keep the sequence strictly increasing regardless.
	times := []time.Time{
		time.Unix(0, 1_000),
		time.Unix(0, 5_000), // forward
		time.Unix(0, 2_000), // step BACK (below the previous mint)
		time.Unix(0, 2_000), // no advance (same instant)
		time.Unix(0, 6_000), // forward again
	}
	i := 0
	src := NewEpochSource(func() time.Time {
		tm := times[i]
		i++
		return tm
	})

	var got []uint64
	for range times {
		got = append(got, src.Next())
	}
	// Wall-clock values would be 1000,5000,2000,2000,6000; the floor forces strict
	// increase: 1000,5000,5001,5002,6000.
	assert.Equal(t, []uint64{1_000, 5_000, 5_001, 5_002, 6_000}, got,
		"Next must be strictly monotone even across a clock step-back")
}

// TestEpochSourceSetFloorRaisesAndLowerIsNoop pins that SetFloor lifts the floor so a
// fresh mint exceeds a persisted session, and that a lower floor cannot pull it back
// down (a stale read must never lower the guarantee).
func TestEpochSourceSetFloorRaisesAndLowerIsNoop(t *testing.T) {
	src := NewEpochSource(func() time.Time { return time.Unix(0, 100) })

	src.SetFloor(10_000)
	assert.Equal(t, uint64(10_001), src.Next(),
		"after SetFloor the next epoch must exceed the floor even though the clock is far below it")

	// A lower floor is ignored; the next epoch still advances from the high-water.
	src.SetFloor(50)
	assert.Equal(t, uint64(10_002), src.Next(), "a lower floor must be a no-op")
}

// TestEpochSourceMintExceedsFloor pins the SP4b use: Mint (== Next) after SetFloor(max+1)
// yields an epoch above every stored session, so a reconcile-timeout DISCONNECTED
// supersedes the stored CONNECTED.
func TestEpochSourceMintExceedsFloor(t *testing.T) {
	src := NewEpochSource(func() time.Time { return time.Unix(0, 1) })
	src.SetFloor(7_777) // max(stored session)+1
	assert.Equal(t, uint64(7_778), src.Mint(), "Mint must exceed the floor set from the read model")
}

// TestEpochSourceConcurrentNextIsUniqueAndMonotone pins that concurrent mints never
// collide and never repeat — the epoch is a process-wide unique ordering key, so a
// duplicate would let two sessions share a SessionId (the dedup-id and supersedence
// both key on it).
func TestEpochSourceConcurrentNextIsUniqueAndMonotone(t *testing.T) {
	// A FIXED clock, so the floor (not the clock) is the only thing that can force
	// uniqueness under contention — the mutex is what's genuinely exercised here.
	src := NewEpochSource(func() time.Time { return time.Unix(0, 1) })

	const goroutines, perG = 8, 500
	var wg sync.WaitGroup
	results := make([][]uint64, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			out := make([]uint64, perG)
			for i := 0; i < perG; i++ {
				out[i] = src.Next()
			}
			results[g] = out
		}(g)
	}
	wg.Wait()

	seen := make(map[uint64]struct{}, goroutines*perG)
	for _, out := range results {
		// Each goroutine observes its own mints strictly increasing (the "AndMonotone"
		// half): a mint never returns a value <= one it returned earlier on the same
		// goroutine, so no thread can see the floor go backwards under contention.
		for i := 1; i < len(out); i++ {
			require.Greater(t, out[i], out[i-1], "a goroutine saw a non-increasing epoch")
		}
		for _, v := range out {
			_, dup := seen[v]
			require.False(t, dup, "epoch %d minted twice — a race on the floor", v)
			seen[v] = struct{}{}
		}
	}
	assert.Len(t, seen, goroutines*perG, "every concurrent mint must be unique")
}
