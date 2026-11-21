// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func stagedRequest() RolloutCreateRequest {
	return RolloutCreateRequest{Token: "staged", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2,
		Cohorts: []RolloutCohort{{Name: "canary", DeviceTokens: []string{"dev-3"}, ManualPromotion: true},
			{Name: "half", Percentage: 50}, {Name: "all", Percentage: 100}}}
}

func TestCohortsGateOnSuccessAndManualPromotionIsIdempotent(t *testing.T) {
	e := newRolloutEnv(t, 4)
	ctx := tenantCtx("acme")
	_, err := e.api.CreateRollout(ctx, stagedRequest())
	require.NoError(t, err)
	v, err := e.api.StartRollout(ctx, "staged")
	require.NoError(t, err)
	require.Equal(t, 1, v.Progress.InFlight)
	require.EqualValues(t, 1, e.assignmentCount(t, "dev-3"))
	_, err = e.api.PromoteRollout(ctx, "staged", 0)
	require.Error(t, err, "an unfinished canary cannot be promoted")
	for range 3 {
		e.step(t, "staged")
	}
	require.Zero(t, e.assignmentCount(t, "dev-0"))
	e.finishCurrent(t, ota.StateUpdated, "dev-3")
	require.Equal(t, PauseCohortReady, e.step(t, "staged").Reason)
	_, err = e.api.ResumeRollout(ctx, "staged", nil)
	require.Error(t, err, "ordinary resume cannot bypass promotion")
	for range 3 {
		v, err = e.api.PromoteRollout(ctx, "staged", 0)
		require.NoError(t, err)
		require.Equal(t, 1, v.Rollout.CurrentCohort)
	}
	require.Equal(t, 1, v.Progress.InFlight)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.step(t, "staged")
	e.step(t, "staged")
	v = e.rollout(t, "staged")
	require.Equal(t, 2, v.Rollout.CurrentCohort)
	require.Equal(t, 2, v.Progress.InFlight, "the last cohort fills exactly the cap")
	for range 4 {
		e.step(t, "staged")
	}
	for i := range 4 {
		require.EqualValues(t, 1, e.assignmentCount(t, fmt.Sprintf("dev-%d", i)), "no cohort redispatches a device")
	}
}

func TestFailedUnknownAndSkippedCanariesNeverPromote(t *testing.T) {
	for _, state := range []ota.State{ota.StateFailed, ota.StateUnknown, ota.StateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			e := newRolloutEnv(t, 4)
			ctx := tenantCtx("acme")
			req := stagedRequest()
			req.Cohorts[0].ManualPromotion = false
			_, err := e.api.CreateRollout(ctx, req)
			require.NoError(t, err)
			_, err = e.api.StartRollout(ctx, req.Token)
			require.NoError(t, err)
			e.finishCurrent(t, state, "dev-3")
			for range 3 {
				e.step(t, req.Token)
			}
			require.Zero(t, e.assignmentCount(t, "dev-0"))
			_, err = e.api.PromoteRollout(ctx, req.Token, 0)
			require.Error(t, err)
		})
	}
}

func TestFailureRateHaltsBeforeDispatchAndNeedsExplicitIdempotentOverride(t *testing.T) {
	e := newRolloutEnv(t, 6)
	ctx := tenantCtx("acme")
	req := RolloutCreateRequest{Token: "rate", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2,
		FailureRate: &RolloutFailureRate{Percentage: 49, WindowSeconds: 600, MinimumSamples: 2}}
	_, err := e.api.CreateRollout(ctx, req)
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, req.Token)
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.finishCurrent(t, ota.StateFailed, "dev-1")
	// Attempts use the server receipt time, never device-provided report times.
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("1 = 1").Update("updated_at", e.clock).Error)
	res := e.step(t, req.Token)
	require.Equal(t, PauseFailureRate, res.Reason)
	require.Zero(t, res.Dispatched)
	v := e.rollout(t, req.Token)
	require.Equal(t, RolloutPaused, v.Rollout.State)
	require.Equal(t, 1, v.Rollout.HaltFailures)
	require.Equal(t, 2, v.Rollout.HaltSamples)
	_, err = e.api.ResumeRollout(ctx, req.Token, nil)
	require.Error(t, err)
	for range 3 {
		v, err = e.api.ResumeHaltedRollout(ctx, req.Token, 1, nil)
		require.NoError(t, err)
		require.Equal(t, RolloutRunning, v.Rollout.State)
		require.LessOrEqual(t, v.Progress.Occupied(), 2)
	}
	require.Equal(t, 1, v.Rollout.HaltFailures, "override keeps the evidence")
	require.EqualValues(t, 1, e.assignmentCount(t, "dev-2"))
	e.finishCurrent(t, ota.StateFailed, "dev-2")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("device_token = ?", "dev-2").Update("updated_at", e.clock).Error)
	require.Equal(t, PauseFailureRate, e.step(t, req.Token).Reason)
	_, err = e.api.ResumeHaltedRollout(ctx, req.Token, 1, nil)
	require.Error(t, err, "a stale override must never release a later halt")
	require.Zero(t, e.assignmentCount(t, "dev-4"))
}

func TestRateHaltCanResumeWhenTheCountThresholdAlsoNeedsRaising(t *testing.T) {
	e := newRolloutEnv(t, 6)
	ctx := tenantCtx("acme")
	_, err := e.api.CreateRollout(ctx, RolloutCreateRequest{Token: "both", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 3,
		FailureThreshold: ptr(2), FailureRate: &RolloutFailureRate{Percentage: 49, WindowSeconds: 600, MinimumSamples: 2}})
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, "both")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.finishCurrent(t, ota.StateFailed, "dev-1")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("device_token IN ?", []string{"dev-0", "dev-1"}).Update("updated_at", e.clock).Error)
	require.Equal(t, PauseFailureRate, e.step(t, "both").Reason)
	e.advance(11 * time.Minute)
	e.finishCurrent(t, ota.StateFailed, "dev-2")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("device_token = ?", "dev-2").Update("updated_at", e.clock).Error)
	_, err = e.api.ResumeHaltedRollout(ctx, "both", 1, nil)
	require.Error(t, err)
	v, err := e.api.ResumeHaltedRollout(ctx, "both", 1, ptr(3))
	require.NoError(t, err)
	require.EqualValues(t, 3, v.Rollout.FailureThreshold.Int32)
	require.Equal(t, RolloutRunning, v.Rollout.State)
}

func TestHaltResumeRetryAfterCompletionIsIdempotent(t *testing.T) {
	e := newRolloutEnv(t, 2)
	ctx := tenantCtx("acme")
	_, err := e.api.CreateRollout(ctx, RolloutCreateRequest{Token: "done", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2,
		FailureRate: &RolloutFailureRate{Percentage: 49, WindowSeconds: 600, MinimumSamples: 2}})
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, "done")
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.finishCurrent(t, ota.StateFailed, "dev-1")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("1 = 1").Update("updated_at", e.clock).Error)
	require.Equal(t, PauseFailureRate, e.step(t, "done").Reason)
	for range 3 {
		v, err := e.api.ResumeHaltedRollout(ctx, "done", 1, nil)
		require.NoError(t, err)
		require.Equal(t, RolloutCompleted, v.Rollout.State)
	}
	_, err = e.api.ResumeRollout(ctx, "done", nil)
	require.Error(t, err, "the existing ordinary resume contract is unchanged")
	_, err = e.api.ResumeHaltedRollout(ctx, "done", 1, ptr(3))
	require.Error(t, err, "a retry cannot silently accept a changed threshold after completion")
}

func TestSkippedCanaryCannotReleaseLaterCohorts(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.devices.disabled["acme/dev-3"] = true
	req := stagedRequest()
	req.Cohorts[0].ManualPromotion = false
	_, err := e.api.CreateRollout(tenantCtx("acme"), req)
	require.NoError(t, err)
	_, err = e.api.StartRollout(tenantCtx("acme"), req.Token)
	require.NoError(t, err)
	for range 3 {
		e.step(t, req.Token)
	}
	require.Equal(t, 1, e.rollout(t, req.Token).Progress.Skipped)
	require.Zero(t, e.assignmentCount(t, "dev-0"))
}

func TestStaleCohortCandidateCannotClaimADeviceTwice(t *testing.T) {
	e := newRolloutEnv(t, 4)
	ctx := tenantCtx("acme")
	_, err := e.api.CreateRollout(ctx, stagedRequest())
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, "staged")
	require.NoError(t, err)
	var target UpdateRolloutTarget
	require.NoError(t, e.api.RDB.DB(ctx).Where("device_token = ?", "dev-3").First(&target).Error)
	facts, found, err := e.devices.DeviceOf(ctx, "acme", "dev-3")
	require.NoError(t, err)
	v := e.rollout(t, "staged")
	require.NoError(t, e.api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		_, _, _, err := e.api.dispatchOne(ctx, tx, &v.Artifact, []string{"gateway"}, candidate{target: target, facts: facts, found: found}, v.Rollout.StartedAt)
		require.ErrorIs(t, err, errTargetTaken, "compare-and-set rejects a stale candidate even if another replica already assigned it")
		return nil
	}))
	require.EqualValues(t, 1, e.assignmentCount(t, "dev-3"))
}

func TestRateWindowMinimumSampleAndUnknownDenominator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   ota.State
		age     time.Duration
		minimum int32
		percent int32
		halt    bool
	}{
		{"below sample", ota.StateFailed, 0, 3, 20, false},
		{"equal threshold", ota.StateFailed, 0, 2, 50, false},
		{"outside window", ota.StateFailed, 601 * time.Second, 1, 20, false},
		{"window boundary", ota.StateFailed, 600 * time.Second, 2, 20, true},
		{"unknown is bad", ota.StateUnknown, 0, 2, 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRolloutEnv(t, 4)
			ctx := tenantCtx("acme")
			_, err := e.api.CreateRollout(ctx, RolloutCreateRequest{Token: "r", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2,
				FailureRate: &RolloutFailureRate{Percentage: tc.percent, MinimumSamples: tc.minimum, WindowSeconds: 600}})
			require.NoError(t, err)
			_, err = e.api.StartRollout(ctx, "r")
			require.NoError(t, err)
			e.finishCurrent(t, ota.StateUpdated, "dev-0")
			e.finishCurrent(t, tc.state, "dev-1")
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("1 = 1").Update("updated_at", e.clock.Add(-tc.age)).Error)
			res := e.step(t, "r")
			require.Equal(t, tc.halt, res.Reason == PauseFailureRate)
		})
	}
}

func TestSafetyPlansRejectAmbiguousTargetsAndInvalidBounds(t *testing.T) {
	e := newRolloutEnv(t, 4)
	ctx := tenantCtx("acme")
	for _, cohorts := range [][]RolloutCohort{
		{{Name: "missing-final", DeviceTokens: []string{"dev-0"}}},
		{{Name: "same", Percentage: 10}, {Name: "same", Percentage: 100}},
		{{Name: "one", DeviceTokens: []string{"dev-0"}}, {Name: "two", DeviceTokens: []string{"dev-0"}}, {Name: "all", Percentage: 100}},
		{{Name: "both", DeviceTokens: []string{"dev-0"}, Percentage: 100}},
		{{Name: "large", Percentage: 101}},
	} {
		req := stagedRequest()
		req.Cohorts = cohorts
		_, err := e.api.CreateRollout(ctx, req)
		require.Error(t, err)
	}
	req := stagedRequest()
	req.Cohorts[0].DeviceTokens = []string{"outside"}
	_, err := e.api.CreateRollout(ctx, req)
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, req.Token)
	require.Error(t, err)
	require.Equal(t, RolloutDraft, e.rollout(t, req.Token).Rollout.State)
	for _, f := range []RolloutFailureRate{{0, 600, 2}, {101, 600, 2}, {20, 0, 2}, {20, 86401, 2}, {20, 600, 0}} {
		req := stagedRequest()
		req.FailureRate = &f
		_, err := e.api.CreateRollout(ctx, req)
		require.Error(t, err)
	}
}
