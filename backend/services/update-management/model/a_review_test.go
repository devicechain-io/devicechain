// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"github.com/devicechain-io/dc-microservice/ota"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestFailedAndUnknownCanariesPauseVisibly(t *testing.T) {
	for _, state := range []ota.State{ota.StateFailed, ota.StateTimedOut, ota.StateUnknown} {
		t.Run(string(state), func(t *testing.T) {
			e := newRolloutEnv(t, 4)
			e.api.RolloutSafety = true
			ctx := tenantCtx("acme")
			req := stagedRequest()
			req.Cohorts[0].ManualPromotion = false
			_, err := e.api.CreateRollout(ctx, req)
			require.NoError(t, err)
			_, err = e.api.StartRollout(ctx, req.Token)
			require.NoError(t, err)
			e.finishCurrent(t, state, "dev-3")
			res := e.step(t, req.Token)
			require.Equal(t, RolloutPaused, res.Transition)
			require.Equal(t, "COHORT_FAILED", res.Reason)
			require.Zero(t, e.assignmentCount(t, "dev-0"))
			_, err = e.api.ResumeRollout(ctx, req.Token, nil)
			require.Error(t, err, "ordinary resume must not bypass the cohort decision")
			_, err = e.api.PromoteRollout(ctx, req.Token, 0, false)
			require.Error(t, err, "failed promotion requires an explicit override")
			for range 2 {
				v, err := e.api.PromoteRollout(ctx, req.Token, 0, true)
				require.NoError(t, err)
				require.Equal(t, 1, v.Rollout.CurrentCohort)
				require.LessOrEqual(t, v.Progress.Occupied(), req.MaxInFlight)
				if state == ota.StateUnknown {
					require.Equal(t, 1, v.Progress.Unknown)
				}
			}
		})
	}
}

func TestSkippedAndCancelledCanariesReleaseLaterCohorts(t *testing.T) {
	for _, skipped := range []bool{true, false} {
		t.Run(fmt.Sprint("skipped=", skipped), func(t *testing.T) {
			e := newRolloutEnv(t, 4)
			e.api.RolloutSafety = true
			e.devices.disabled["acme/dev-3"] = skipped
			req := stagedRequest()
			req.Cohorts[0].ManualPromotion = false
			_, err := e.api.CreateRollout(tenantCtx("acme"), req)
			require.NoError(t, err)
			_, err = e.api.StartRollout(tenantCtx("acme"), req.Token)
			require.NoError(t, err)
			if !skipped {
				e.finishCurrent(t, ota.StateCancelled, "dev-3")
			}
			for range 3 {
				e.step(t, req.Token)
			}
			require.Equal(t, 1, e.rollout(t, req.Token).Rollout.CurrentCohort)
			require.EqualValues(t, 1, e.assignmentCount(t, "dev-0"))
			e.finishCurrent(t, ota.StateUpdated, "dev-0")
			for range 3 {
				e.step(t, req.Token)
			}
			e.finishCurrent(t, ota.StateUpdated, "dev-1", "dev-2")
			e.step(t, req.Token)
			require.Equal(t, RolloutCompleted, e.rollout(t, req.Token).Rollout.State)
		})
	}
}

func TestFailureRateRejectsUnreachableHundredPercentThreshold(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.api.RolloutSafety = true
	req := stagedRequest()
	req.FailureRate = &RolloutFailureRate{Percentage: 100, WindowSeconds: 600, MinimumSamples: 1}
	_, err := e.api.CreateRollout(tenantCtx("acme"), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "1..99")
	req.FailureRate.Percentage = 99
	_, err = e.api.CreateRollout(tenantCtx("acme"), req)
	require.NoError(t, err)
}

func TestAcknowledgedUnknownVersionBumpDoesNotRehalt(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.api.RolloutSafety = true
	ctx := tenantCtx("acme")
	req := RolloutCreateRequest{Token: "rate", ArtifactToken: "fw-2", GroupToken: "fleet", MaxInFlight: 2,
		FailureRate: &RolloutFailureRate{Percentage: 49, WindowSeconds: 600, MinimumSamples: 2}}
	_, err := e.api.CreateRollout(ctx, req)
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, req.Token)
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUpdated, "dev-0")
	e.finishCurrent(t, ota.StateUnknown, "dev-1")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("1 = 1").Update("updated_at", e.clock).Error)
	require.Equal(t, PauseFailureRate, e.step(t, req.Token).Reason)
	_, err = e.api.ResumeHaltedRollout(ctx, req.Token, 1, nil)
	require.NoError(t, err)
	v := e.rollout(t, req.Token)
	require.Equal(t, 1, v.Progress.Unknown, "UNKNOWN retains its slot after override")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("device_token = ?", "dev-1").
		Updates(map[string]any{"version": gorm.Expr("version + 1"), "updated_at": e.clock}).Error)
	e.step(t, req.Token)
	v = e.rollout(t, req.Token)
	require.Equal(t, RolloutRunning, v.Rollout.State)
	require.Equal(t, 1, v.Rollout.HaltSequence)
	// A genuinely different bad state on that attempt remains new evidence.
	e.finishCurrent(t, ota.StateFailed, "dev-1")
	require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateAttempt{}).Where("device_token = ?", "dev-1").Update("updated_at", e.clock).Error)
	require.Equal(t, PauseFailureRate, e.step(t, req.Token).Reason)
	require.Equal(t, 2, e.rollout(t, req.Token).Rollout.HaltSequence)
}

func TestSafetyCreateAndStartFailClosedWithoutInstanceOptIn(t *testing.T) {
	for _, rateOnly := range []bool{false, true} {
		t.Run(fmt.Sprint("rateOnly=", rateOnly), func(t *testing.T) {
			e := newRolloutEnv(t, 4)
			e.api.RolloutSafety = false
			ctx := tenantCtx("acme")
			req := stagedRequest()
			if rateOnly {
				req.Cohorts = nil
				req.FailureRate = &RolloutFailureRate{Percentage: 25, WindowSeconds: 600, MinimumSamples: 1}
			}
			_, err := e.api.CreateRollout(ctx, req)
			require.Error(t, err)
			require.Contains(t, err.Error(), "infrastructure.updateManagement.rolloutSafety")
			// A stored draft created on an enabled replica must also be refused here.
			cohorts, rate := req.Cohorts, req.FailureRate
			req.Cohorts, req.FailureRate = nil, nil
			v, err := e.api.CreateRollout(ctx, req)
			require.NoError(t, err, "ordinary rollouts remain available")
			cols := map[string]any{}
			if rateOnly {
				cols["failure_rate_plan"] = safetyJSON(rate)
			} else {
				cols["cohort_plan"] = safetyJSON(cohorts)
			}
			require.NoError(t, e.api.RDB.DB(ctx).Model(&UpdateRollout{}).Where("id = ?", v.Rollout.ID).Updates(cols).Error)
			_, err = e.api.StartRollout(ctx, req.Token)
			require.Error(t, err)
			require.Contains(t, err.Error(), "infrastructure.updateManagement.rolloutSafety")
			require.Equal(t, RolloutDraft, e.rollout(t, req.Token).Rollout.State)
			require.Empty(t, e.targets(t, req.Token, nil), "refusal freezes and dispatches nothing")
		})
	}
}

func TestUnknownAtCapPausesBeforePendingCanariesStall(t *testing.T) {
	e := newRolloutEnv(t, 4)
	e.api.RolloutSafety = true
	ctx := tenantCtx("acme")
	req := stagedRequest()
	req.MaxInFlight = 1
	req.Cohorts = []RolloutCohort{{Name: "canaries", DeviceTokens: []string{"dev-0", "dev-1"}}, {Name: "all", Percentage: 100}}
	_, err := e.api.CreateRollout(ctx, req)
	require.NoError(t, err)
	_, err = e.api.StartRollout(ctx, req.Token)
	require.NoError(t, err)
	e.finishCurrent(t, ota.StateUnknown, "dev-0")
	require.Equal(t, PauseCohortFailed, e.step(t, req.Token).Reason)
	_, err = e.api.PromoteRollout(ctx, req.Token, 0, true)
	require.Error(t, err, "pending canaries cannot be bypassed")
	// Abandoning the unknown attempt releases its slot. Resume can then finish
	// this cohort without bypassing any pending device.
	e.finishCurrent(t, ota.StateCancelled, "dev-0")
	v, err := e.api.ResumeRollout(ctx, req.Token, nil)
	require.NoError(t, err)
	require.Equal(t, 0, v.Rollout.CurrentCohort)
	require.EqualValues(t, 1, e.assignmentCount(t, "dev-1"))
}
