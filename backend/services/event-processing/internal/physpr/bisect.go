// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package physpr

import (
	"context"
	"errors"
	"fmt"
)

// MaxVersions bounds the history one bisect accepts. A bisect costs about log2(n)+2 replays, so
// 64 versions is at most eight replays plus the two endpoints: a finite, self-limiting job.
const MaxVersions = 64

// ErrNoChange is returned when the oldest and newest versions produce the same outcome: there is
// no flip to find. It is a finding, not a failure, and callers should print it as one.
var ErrNoChange = errors.New("the outcome is the same under the oldest and the newest version; there is no change to bisect")

// Probe replays version i (0 is the oldest) and reports whether the outcome under study holds, for
// example "an alarm is raised on pump-17 at 14:20". It must be deterministic for a given i.
type Probe func(ctx context.Context, i int) (bool, error)

// Step records one probe, for the report's trail.
type Step struct {
	Version int
	Holds   bool
}

// BisectResult names the version at which the outcome flipped.
type BisectResult struct {
	// First is the index of the first version, on the path the search took, whose outcome differs
	// from the oldest version's; version First-1 produces the oldest version's outcome. The
	// change that introduced the flip is exactly version First.
	First int
	// OldestHolds is the outcome under the oldest version; the flip is to !OldestHolds.
	OldestHolds bool
	// Trail lists the probes in the order they ran.
	Trail []Step
}

// Bisect finds an adjacent pair of versions (First-1, First) whose outcomes differ, given that the
// oldest and newest versions differ. It is git bisect over replayed outcomes: O(log n) probes,
// each memoised. n is the number of versions.
//
// The invariant that makes the answer trustworthy holds on every iteration: probe(lo) is the oldest
// outcome and probe(hi) is the other one. Whatever the middle probes say, the loop ends with
// hi == lo+1, so the result is always a real flip between two adjacent versions. What the search
// cannot know without probing every version is whether there is more than one flip: it is a real
// flip, and the only one only if the history has a single. Callers that need that proof can probe
// every version, at n replays instead of log2(n).
func Bisect(ctx context.Context, n int, probe Probe) (BisectResult, error) {
	if n < 2 {
		return BisectResult{}, fmt.Errorf("bisect needs at least two versions, got %d", n)
	}
	if n > MaxVersions {
		return BisectResult{}, fmt.Errorf("bisect accepts at most %d versions, got %d", MaxVersions, n)
	}
	var res BisectResult
	memo := map[int]bool{}
	ask := func(i int) (bool, error) {
		if v, ok := memo[i]; ok {
			return v, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		v, err := probe(ctx, i)
		if err != nil {
			return false, fmt.Errorf("replaying version %d: %w", i, err)
		}
		memo[i] = v
		res.Trail = append(res.Trail, Step{Version: i, Holds: v})
		return v, nil
	}
	oldest, err := ask(0)
	if err != nil {
		return res, err
	}
	newest, err := ask(n - 1)
	if err != nil {
		return res, err
	}
	res.OldestHolds = oldest
	if oldest == newest {
		return res, ErrNoChange
	}
	lo, hi := 0, n-1
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		v, err := ask(mid)
		if err != nil {
			return res, err
		}
		if v == oldest {
			lo = mid
		} else {
			hi = mid
		}
	}
	res.First = hi
	return res, nil
}
