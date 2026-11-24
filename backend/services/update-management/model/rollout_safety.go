// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/ledger/waves"
	"github.com/devicechain-io/dc-microservice/ota"
	"gorm.io/gorm"
)

// RolloutCohort selects an explicit canary set or a cumulative fleet percentage.
// ManualPromotion gates departure once all members succeed or are skipped/cancelled.
type RolloutCohort struct {
	Name            string
	DeviceTokens    []string
	Percentage      int32
	ManualPromotion bool
}

// RolloutFailureRate counts UPDATED, FAILED, TIMED_OUT and UNKNOWN attempts whose
// server update time lies in the trailing window. Only UPDATED is a success.
type RolloutFailureRate struct {
	Percentage     int32
	WindowSeconds  int32
	MinimumSamples int32
}

func validateSafety(req RolloutCreateRequest) error {
	if err := waves.Validate(wavePlan(req.Cohorts)); err != nil {
		return refuse(CodeInvalidRollout, "%v", err)
	}
	if f := req.FailureRate; f != nil && (f.Percentage < 1 || f.Percentage > 99 || f.WindowSeconds < 1 || f.WindowSeconds > 86400 || f.MinimumSamples < 1 || f.MinimumSamples > MaxRolloutTargets) {
		return refuse(CodeInvalidRollout, "failure rate requires percentage 1..99, windowSeconds 1..86400 and minimumSamples 1..10000")
	}
	return nil
}

func wavePlan(cohorts []RolloutCohort) []waves.Cohort {
	plan := make([]waves.Cohort, len(cohorts))
	for i, c := range cohorts {
		plan[i] = waves.Cohort{Name: c.Name, DeviceTokens: c.DeviceTokens, Percentage: c.Percentage}
	}
	return plan
}

func safetyJSON(v any) sql.NullString {
	b, _ := json.Marshal(v) // these structs contain only JSON primitives
	return sql.NullString{String: string(b), Valid: true}
}

// Cohorts decodes the immutable plan, failing loudly on corrupt stored data.
func (r UpdateRollout) Cohorts() ([]RolloutCohort, error) {
	var out []RolloutCohort
	if r.CohortPlan.Valid {
		if err := json.Unmarshal([]byte(r.CohortPlan.String), &out); err != nil {
			return nil, fmt.Errorf("unreadable cohort plan: %w", err)
		}
	}
	return out, nil
}

// FailureRate decodes the optional safety policy.
func (r UpdateRollout) FailureRate() (*RolloutFailureRate, error) {
	if !r.FailureRatePlan.Valid {
		return nil, nil
	}
	var out RolloutFailureRate
	if err := json.Unmarshal([]byte(r.FailureRatePlan.String), &out); err != nil {
		return nil, fmt.Errorf("unreadable failure rate policy: %w", err)
	}
	return &out, nil
}

// freezeCohorts assigns every frozen device exactly once. Percentages are
// cumulative, rounded up, with a stable order inherited from the frozen group.
func freezeCohorts(r UpdateRollout, devices []string) (map[string]int, error) {
	plan, err := r.Cohorts()
	if err != nil {
		return nil, err
	}
	out, err := waves.Assign(wavePlan(plan), devices)
	if err != nil {
		return nil, refuse(CodeInvalidRollout, "%v", err)
	}
	return out, nil
}

func cohortProgress(tx *gorm.DB, r *UpdateRollout) (RolloutProgress, error) {
	var progress RolloutProgress
	var rows []struct {
		Status       string
		AttemptState sql.NullString
	}
	err := tx.Model(&UpdateRolloutTarget{}).
		Select("update_rollout_targets.status, update_attempts.state AS attempt_state").
		Joins("LEFT JOIN update_attempts ON update_attempts.assignment_id = update_rollout_targets.assignment_id AND update_attempts.tenant_id = update_rollout_targets.tenant_id").
		Where("update_rollout_targets.rollout_id = ? AND update_rollout_targets.cohort = ?", r.ID, r.CurrentCohort).
		Scan(&rows).Error
	if err != nil {
		return progress, err
	}
	for _, row := range rows {
		progress.add(outcomeOf(row.Status, row.AttemptState), 1)
	}
	return progress, nil
}

// PromoteRollout advances the expected settled cohort. Failed or UNKNOWN outcomes
// require overrideFailure. UNKNOWN still occupies a slot after promotion. Repeating an old
// promotion cannot accidentally promote a second cohort, even on another replica.
func (api *Api) PromoteRollout(ctx context.Context, token string, expected int, overrideFailure bool) (*UpdateRolloutView, error) {
	var advanced uint
	_, err := api.transition(ctx, token, func(tx *gorm.DB, r *UpdateRollout) (map[string]any, error) {
		plan, err := r.Cohorts()
		if err != nil {
			return nil, err
		}
		if expected < 0 || expected >= len(plan)-1 {
			return nil, refuse(CodeRolloutStateConflict, "no next cohort")
		}
		if r.CurrentCohort > expected {
			return nil, nil
		}
		if r.CurrentCohort != expected || (r.State != RolloutRunning && !(r.State == RolloutPaused && (r.PauseReason == PauseCohortReady || r.PauseReason == PauseCohortFailed))) {
			return nil, refuse(CodeRolloutStateConflict, "cohort cannot be promoted in this state")
		}
		p, err := cohortProgress(tx, r)
		if err != nil {
			return nil, err
		}
		if p.Total == 0 || p.Pending > 0 || p.InFlight > 0 {
			return nil, refuse(CodeRolloutStateConflict, "cohort targets are still pending or in flight")
		}
		if p.Failures() > 0 && !overrideFailure {
			return nil, refuse(CodeRolloutStateConflict, "failed or unknown cohort outcomes require overrideFailure")
		}
		advanced = r.ID
		return map[string]any{"current_cohort": expected + 1, "state": RolloutRunning, "pause_reason": ""}, nil
	})
	if err != nil {
		return nil, err
	}
	if advanced != 0 {
		api.stepInline(ctx, advanced)
	}
	return api.RolloutByToken(ctx, token)
}

// ResumeHaltedRollout explicitly overrides one numbered rate halt. The full
// trailing-window accounting is preserved; a new bad outcome may halt it again.
func (api *Api) ResumeHaltedRollout(ctx context.Context, token string, sequence int, failureThreshold *int) (*UpdateRolloutView, error) {
	return api.resumeRollout(ctx, token, failureThreshold, &sequence)
}

// rateHalt is evaluated under the rollout row lock before spending any slots.
func (api *Api) rateHalt(tx *gorm.DB, r *UpdateRollout) (bool, error) {
	f, err := r.FailureRate()
	if err != nil || f == nil {
		return false, err
	}
	now := api.now()
	var rows []struct {
		ID    uint
		State string
	}
	err = tx.Model(&UpdateRolloutTarget{}).Select("update_attempts.id, update_attempts.state").
		Joins("JOIN update_attempts ON update_attempts.assignment_id = update_rollout_targets.assignment_id AND update_attempts.tenant_id = update_rollout_targets.tenant_id").
		Where("update_rollout_targets.rollout_id = ? AND update_rollout_targets.status = ? AND update_attempts.updated_at >= ? AND update_attempts.updated_at <= ?",
			r.ID, TargetDispatched, now.Add(-time.Duration(f.WindowSeconds)*time.Second), now).Scan(&rows).Error
	if err != nil {
		return false, err
	}
	var acknowledged map[string]bool
	if r.RateOverrideAt.Valid && r.HaltFailureEvidence.Valid {
		if err := json.Unmarshal([]byte(r.HaltFailureEvidence.String), &acknowledged); err != nil {
			return false, fmt.Errorf("unreadable halt evidence: %w", err)
		}
	}
	evidence := map[string]bool{}
	samples, failures, newFailure := 0, 0, false
	for _, row := range rows {
		switch ota.State(row.State) {
		case ota.StateUpdated:
			samples++
		case ota.StateFailed, ota.StateTimedOut, ota.StateUnknown:
			samples++
			failures++
			key := fmt.Sprintf("%d:%s", row.ID, row.State)
			evidence[key] = true
			if !acknowledged[key] {
				newFailure = true
			}
		}
	}
	if samples < int(f.MinimumSamples) || failures*100 <= samples*int(f.Percentage) || !newFailure {
		return false, nil
	}
	err = tx.Model(&UpdateRollout{ID: r.ID, Token: r.Token}).Where("id = ?", r.ID).
		Updates(map[string]any{"halt_sequence": r.HaltSequence + 1, "halt_failures": failures, "halt_samples": samples,
			"halt_failure_evidence": safetyJSON(evidence)}).Error
	return err == nil, err
}

// requireRolloutSafety fails closed during mixed-version upgrades. This setting
// comes only from the mounted instance configuration, never a request or tenant.
func (api *Api) requireRolloutSafety(needed bool) error {
	if needed && !api.RolloutSafety {
		return refuse(CodeRolloutSafetyDisabled, "cohorts and failureRate require infrastructure.updateManagement.rolloutSafety; enable only after all update-management replicas have upgraded")
	}
	return nil
}
