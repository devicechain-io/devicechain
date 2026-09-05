// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/presence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 THE SETTLE WINDOW WAS A DELAY, NOT A RE-EXAMINATION, and the difference is the whole
// defect. The drain ticks on the reconcile interval for the pod's lifetime and walks
// whatever this source has asserted RIGHT NOW, so the reclassification that was correct at
// minute two — the broker is gone, release the fleet — stayed in force at minute three
// when the broker was back. With peers that is not churn but harm: the peers' reconcile
// re-asserts every row this pod releases, this pod releases them again on the next tick,
// and in the gap the inactivity sweep marks every connected-but-quiet device offline.
//
// These pin the question being asked at all, and asked BEFORE the first pass.

// recordingRunner is the drain seam, so a pass that must not happen is observable as an
// absence rather than inferred from a metric.
type recordingRunner struct {
	runs int
	last time.Time
	err  error
}

func (r *recordingRunner) Run(_ context.Context, now time.Time) error {
	r.runs++
	r.last = now
	return r.err
}

// TestARecoveredBrokerEndsTheRunInsteadOfDraining is the case the whole fix exists for.
func TestARecoveredBrokerEndsTheRunInsteadOfDraining(t *testing.T) {
	drain := &recordingRunner{}
	recovered := 0
	r := recheckBroker{
		drain:     drain,
		reachable: func(context.Context) bool { return true },
		recovered: func() { recovered++ },
	}

	require.NoError(t, r.Run(context.Background(), time.Now()))
	assert.Equal(t, 0, drain.runs,
		"a pod whose broker is back must not release a single device; its peers would re-assert every one")
	assert.Equal(t, 1, recovered, "the tap-less run must be ended rather than continued")
}

// TestAStillUnreachableBrokerDrains is the counterweight, and it is the half that keeps
// the reclassification worth anything: a recheck that answered "reachable" too readily
// would silently restore the frozen fleet this change set out to release.
func TestAStillUnreachableBrokerDrains(t *testing.T) {
	drain := &recordingRunner{}
	recovered := 0
	r := recheckBroker{
		drain:     drain,
		reachable: func(context.Context) bool { return false },
		recovered: func() { recovered++ },
	}

	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, r.Run(context.Background(), now))
	assert.Equal(t, 1, drain.runs)
	assert.Equal(t, now, drain.last, "the drain must be given the pass's own clock, unchanged")
	assert.Equal(t, 0, recovered)
}

// TestTheDrainsErrorIsTheLoopsError. The loop logs and keeps going on a failed pass; a
// wrapper that swallowed the error would turn every unreachable tenant into silence.
func TestTheDrainsErrorIsTheLoopsError(t *testing.T) {
	boom := errors.New("listing tenants failed")
	r := recheckBroker{
		drain:     &recordingRunner{err: boom},
		reachable: func(context.Context) bool { return false },
		recovered: func() { t.Fatal("recovered must not fire while the broker is unreachable") },
	}
	assert.ErrorIs(t, r.Run(context.Background(), time.Now()), boom)
}

// TestARecheckOnlyLoopHasNothingToDrain. An instance whose drain has no endpoints to read
// still gets the recheck, so the one bail reason that can repair itself is not the one
// silently left to a manual restart.
func TestARecheckOnlyLoopHasNothingToDrain(t *testing.T) {
	recovered := 0
	r := recheckBroker{
		drain:     nil,
		reachable: func(context.Context) bool { return false },
		recovered: func() { recovered++ },
	}
	require.NoError(t, r.Run(context.Background(), time.Now()), "a nil drain is a no-op, not a panic")
	assert.Equal(t, 0, recovered)

	r.reachable = func(context.Context) bool { return true }
	require.NoError(t, r.Run(context.Background(), time.Now()))
	assert.Equal(t, 1, recovered, "the recheck must still end the run when there is nothing to drain")
}

// TestTheRecheckRunsBeforeTheFirstPass is the settle window's actual contract, driven
// through the real loop rather than asserted about it.
//
// 🔴 THE FIRST PASS IS THE ONE THE WINDOW WAS FOR. A recheck bolted on from the second
// tick would still release a whole fleet at T+120s on a broker that came back at T+90s,
// which is the exact scenario the window is documented as covering.
func TestTheRecheckRunsBeforeTheFirstPass(t *testing.T) {
	drain := &recordingRunner{}
	checked := make(chan struct{}, 4)
	r := recheckBroker{
		drain: drain,
		reachable: func(context.Context) bool {
			select {
			case checked <- struct{}{}:
			default:
			}
			return true
		},
		recovered: func() {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		presence.RunDemoteLoop(ctx, r, time.Hour, time.Millisecond, time.Now)
	}()

	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop's first pass never asked whether the broker was back")
	}
	cancel()
	<-done
	assert.Equal(t, 0, drain.runs, "the first pass drained despite the broker being reachable")
}

// TestOnlyAnUnreachableBrokerGetsARecheck pins which reason is wrapped, because the other
// two instance-wide reasons cannot stop being true inside this process and a dial would
// answer a question nobody asked.
//
// 🔑 THE ASYMMETRY IS PRINCIPLED, NOT AN OVERSIGHT. `enabled: false` and a missing
// system-account credential are read once from the mounted configuration at startup, and a
// configuration change rolls the pod — so for those the settle window is a delay that lets
// the REPLACEMENT arrive, which is the right mechanism and the only possible one. An
// unreachable broker is the only reason whose truth this process can re-establish itself,
// and therefore the only one where a delay without a re-check buys nothing.
func TestOnlyAnUnreachableBrokerGetsARecheck(t *testing.T) {
	assert.Equal(t, presence.TapOffBrokerUnreachable, presence.TapOffReason("broker_unreachable"),
		"the reason this wiring keys on")

	for _, r := range presence.AllTapOffReasons() {
		wrapped := r == presence.TapOffBrokerUnreachable
		if reasonIsInstanceWide(r) && !wrapped {
			// disabled / no_system_credential: configuration, re-read only by a new pod.
			assert.True(t, r == presence.TapOffDisabled || r == presence.TapOffNoSystemCredential,
				"an instance-wide reason other than the broker one appeared without a decision: %s", r)
		}
	}
}
