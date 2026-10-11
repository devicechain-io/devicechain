// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package nldraft

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slowInferer answers each call after the scripted delay — or, if the call's context ends
// first, with the timeout the Inferer contract requires (wrapping ErrTimedOut). A negative
// delay means the call never answers on its own.
type slowInferer struct {
	delays []time.Duration
	calls  int
}

func (s *slowInferer) Infer(ctx context.Context, _, _, _ string) (InferOutput, error) {
	d := s.delays[min(s.calls, len(s.delays)-1)]
	s.calls++
	var wait <-chan time.Time
	if d >= 0 {
		wait = time.After(d)
	}
	select {
	case <-wait:
		return InferOutput{Candidate: invalidRule, Model: "m", Provider: "p"}, nil
	case <-ctx.Done():
		return InferOutput{}, fmt.Errorf("%w: %v", ErrTimedOut, ctx.Err())
	}
}

func budgeted(inf Inferer, budget, minAttempt time.Duration) *Drafter {
	d := NewDrafter(inf, rules.DefaultLimits(), 3)
	d.budget, d.minAttempt = budget, minAttempt
	return d
}

// The production drafter is bounded by the shared draft budget, not an ad-hoc number.
func TestDraftBudgetIsTheSharedOne(t *testing.T) {
	d := NewDrafter(&slowInferer{delays: []time.Duration{0}}, rules.DefaultLimits(), 0)
	assert.Equal(t, config.AiDraftBudget, d.budget)
	assert.Equal(t, minAttemptBudget, d.minAttempt)
}

// The budget running out DURING a repair attempt ends the draft promptly with the spent
// work and the timed-out notice — not a hang past the request edge.
func TestDraft_BudgetExpiresMidLoop(t *testing.T) {
	inf := &slowInferer{delays: []time.Duration{20 * time.Millisecond, -1}}
	start := time.Now()
	res, err := budgeted(inf, 300*time.Millisecond, time.Millisecond).
		Draft(context.Background(), "acme", Request{Text: "x"})
	require.NoError(t, err)

	assert.Less(t, time.Since(start), 2*time.Second, "the draft outlived its budget")
	assert.Equal(t, 2, inf.calls)
	assert.False(t, res.Unavailable)
	assert.Equal(t, invalidRule, res.RawCandidate)
	require.NotEmpty(t, res.Diagnostics)
	assert.Equal(t, repairTimedOutMessage, res.Diagnostics[len(res.Diagnostics)-1].Message)
}

// The budget running out on the FIRST attempt is the timed-out reason, not "unavailable".
func TestDraft_BudgetExpiresOnFirstAttempt(t *testing.T) {
	inf := &slowInferer{delays: []time.Duration{-1}}
	res, err := budgeted(inf, 100*time.Millisecond, time.Millisecond).
		Draft(context.Background(), "acme", Request{Text: "x"})
	require.NoError(t, err)
	assert.True(t, res.Unavailable)
	assert.Equal(t, timedOutReason, res.UnavailableReason)
}

// Less budget left than an attempt is worth: no new attempt starts. The first attempt
// leaves too little for a repair, so the draft stops at one call with the notice.
func TestDraft_NoAttemptBelowTheMinimum(t *testing.T) {
	inf := &slowInferer{delays: []time.Duration{150 * time.Millisecond}}
	res, err := budgeted(inf, 300*time.Millisecond, 200*time.Millisecond).
		Draft(context.Background(), "acme", Request{Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, 1, inf.calls, "a repair attempt started with less than the minimum budget left")
	assert.Equal(t, invalidRule, res.RawCandidate)
	require.NotEmpty(t, res.Diagnostics)
	assert.Equal(t, repairTimedOutMessage, res.Diagnostics[len(res.Diagnostics)-1].Message)
}

// And when even the first attempt would start below the minimum, none starts at all.
func TestDraft_NoFirstAttemptBelowTheMinimum(t *testing.T) {
	inf := &slowInferer{delays: []time.Duration{0}}
	res, err := budgeted(inf, 50*time.Millisecond, 100*time.Millisecond).
		Draft(context.Background(), "acme", Request{Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, 0, inf.calls)
	assert.True(t, res.Unavailable)
	assert.Equal(t, timedOutReason, res.UnavailableReason)
	assert.EqualValues(t, 0, res.Attempts)
}
