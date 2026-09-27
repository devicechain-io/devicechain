// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package loadtest

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// InvStateCaughtUp: every device the drive accepted an event for has a live-state projection
// whose lastActivityTime has reached that device's last accepted event (within stateSkewPad),
// within Profile.StateTimeout of the drive ending.
//
// It is the only invariant here that reads device-state, which consumes the resolved stream
// independently of event-management: a run can store every event within seconds and still
// leave the live state an hour behind, and ingest-completeness cannot see that.
//
// 🔴 IT IS A LAG GUARD, NOT A THROUGHPUT MEASUREMENT. At the CI tier's load the projection
// catches up within seconds on any build, so a pass there says the live state is not
// broken, not that it is fast. The time it took is reported (stateLagSeconds) so a heavier
// run can be compared across builds.
const InvStateCaughtUp = "state-caught-up"

// stateSkewPad absorbs clock skew between the driver host and the platform — the same
// allowance deriveWindow makes. A device clock that leads the server past its future-skew
// bound has its event time bounded down at resolution, and the projection then holds a
// little less than was sent.
const stateSkewPad = 5 * time.Second

// stateReadChunk bounds how many devices one read asks for.
const stateReadChunk = 200

// stateReader reads device-state for a set of tokens. presenceOracle is the one production
// reader — the presence harness's own query, which carries lastActivityTime — so the two
// harnesses read the projection one way. Abstracted so the wait is unit-testable against a
// scripted reader.
type stateReader interface {
	states(ctx context.Context, tokens []string) (map[string]deviceStateObs, error)
}

// StateResult is what AwaitState observed.
type StateResult struct {
	CaughtUp bool
	Devices  int // devices the drive accepted an event for
	Behind   int // devices not caught up at the last read
	// Example is one device still behind, with what was wanted and what was read.
	Example string
	Elapsed time.Duration // from the start of the wait
	Polls   int
}

// AwaitState polls until every device in want has a lastActivityTime at or after
// want[device]-stateSkewPad, or timeout passes. A device with no row is behind. A read error
// is a run error (fail closed), never "caught up". An EMPTY want is refused as an error:
// asserting nothing is not a pass. A poll of 0 tight-loops, for tests.
func AwaitState(ctx context.Context, r stateReader, want map[string]time.Time, poll, timeout time.Duration) (StateResult, error) {
	if len(want) == 0 {
		return StateResult{}, fmt.Errorf("no accepted events were recorded, so there is nothing to check the live state against")
	}
	tokens := make([]string, 0, len(want))
	for tok := range want {
		tokens = append(tokens, tok)
	}
	sort.Strings(tokens)

	start := time.Now()
	deadline := start.Add(timeout)
	res := StateResult{Devices: len(tokens)}
	for {
		res.Polls++
		behind, example := 0, ""
		for i := 0; i < len(tokens); i += stateReadChunk {
			chunk := tokens[i:min(i+stateReadChunk, len(tokens))]
			obs, err := r.states(ctx, chunk)
			if err != nil {
				res.Elapsed = time.Since(start)
				return res, fmt.Errorf("read the live state: %w", err)
			}
			for _, tok := range chunk {
				o := obs[tok]
				floor := want[tok].Add(-stateSkewPad)
				if o.Present && !o.LastActivity.IsZero() && !o.LastActivity.Before(floor) {
					continue
				}
				behind++
				if example == "" {
					got := "no device-state row"
					if o.Present {
						got = "lastActivityTime " + o.LastActivity.UTC().Format(time.RFC3339Nano)
						if o.LastActivity.IsZero() {
							got = "no lastActivityTime"
						}
					}
					example = fmt.Sprintf("%s: last accepted %s, %s", tok, want[tok].UTC().Format(time.RFC3339Nano), got)
				}
			}
		}
		res.Behind, res.Example, res.Elapsed = behind, example, time.Since(start)
		if behind == 0 {
			res.CaughtUp = true
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, nil
		}
		if poll > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(poll):
			}
		} else if ctx.Err() != nil {
			return res, ctx.Err()
		}
	}
}

// StateInvariant turns the observation into the verdict. lag is how long after the drive
// ended the check finished.
func StateInvariant(res StateResult, lag time.Duration) Invariant {
	if res.CaughtUp {
		return Invariant{Name: InvStateCaughtUp, Passed: true,
			Detail: fmt.Sprintf("the live state of all %d devices reached their last accepted event %.0fs after the drive ended",
				res.Devices, lag.Seconds())}
	}
	return Invariant{Name: InvStateCaughtUp, Passed: false,
		Detail: fmt.Sprintf("BEHIND: %d of %d devices had not reached their last accepted event %.0fs after the drive ended (e.g. %s)",
			res.Behind, res.Devices, lag.Seconds(), res.Example)}
}
