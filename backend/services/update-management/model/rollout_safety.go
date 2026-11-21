// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/ota"
	"gorm.io/gorm"
)

// RolloutCohort selects an explicit canary set or a cumulative fleet percentage.
// ManualPromotion gates departure from this cohort, after every member succeeds.
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
	if len(req.Cohorts) > 20 {
		return refuse(CodeInvalidRollout, "at most 20 cohorts are allowed")
	}
	names, devices := map[string]bool{}, map[string]bool{}
	lastPercentage := int32(0)
	for _, c := range req.Cohorts {
		if c.Name == "" || len(c.Name) > 128 || names[c.Name] || (len(c.DeviceTokens) == 0) == (c.Percentage == 0) {
			return refuse(CodeInvalidRollout, "cohorts need unique names and exactly one selector")
		}
		names[c.Name] = true
		if len(c.DeviceTokens) > MaxRolloutTargets {
			return refuse(CodeInvalidRollout, "too many canary devices")
		}
		for _, d := range c.DeviceTokens {
			if core.ValidateToken(d) != nil || devices[d] || lastPercentage != 0 {
				return refuse(CodeInvalidRollout, "canaries must be unique and precede percentage cohorts")
			}
			devices[d] = true
		}
		if c.Percentage != 0 {
			if c.Percentage <= lastPercentage || c.Percentage > 100 {
				return refuse(CodeInvalidRollout, "percentages must increase through 100")
			}
			lastPercentage = c.Percentage
		}
	}
	if len(req.Cohorts) > 0 && lastPercentage != 100 {
		return refuse(CodeInvalidRollout, "the last cohort must cover 100 percent")
	}
	if f := req.FailureRate; f != nil && (f.Percentage < 1 || f.Percentage > 100 || f.WindowSeconds < 1 || f.WindowSeconds > 86400 || f.MinimumSamples < 1 || f.MinimumSamples > MaxRolloutTargets) {
		return refuse(CodeInvalidRollout, "failure rate requires percentage 1..100, windowSeconds 1..86400 and minimumSamples 1..10000")
	}
	return nil
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
	out := map[string]int{}
	members := map[string]bool{}
	for _, d := range devices {
		members[d] = true
	}
	for i, c := range plan {
		before := len(out)
		for _, d := range c.DeviceTokens {
			if !members[d] {
				return nil, refuse(CodeInvalidRollout, "canary %q is outside the frozen fleet", d)
			}
			out[d] = i
		}
		if c.Percentage > 0 {
			limit := (len(devices)*int(c.Percentage) + 99) / 100
			for _, d := range devices {
				if len(out) >= limit {
					break
				}
				if _, assigned := out[d]; !assigned {
					out[d] = i
				}
			}
		}
		if len(out) == before {
			return nil, refuse(CodeInvalidRollout, "cohort %q is empty for this fleet", c.Name)
		}
	}
	return out, nil
}

func cohortSucceeded(tx *gorm.DB, r *UpdateRollout) (bool, error) {
	var rows []struct {
		Status       string
		AttemptState sql.NullString
	}
	err := tx.Model(&UpdateRolloutTarget{}).
		Select("update_rollout_targets.status, update_attempts.state AS attempt_state").
		Joins("LEFT JOIN update_attempts ON update_attempts.assignment_id = update_rollout_targets.assignment_id AND update_attempts.tenant_id = update_rollout_targets.tenant_id").
		Where("update_rollout_targets.rollout_id = ? AND update_rollout_targets.cohort = ?", r.ID, r.CurrentCohort).
		Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return false, err
	}
	for _, row := range rows {
		if outcomeOf(row.Status, row.AttemptState) != OutcomeSucceeded {
			return false, nil
		}
	}
	return true, nil
}

// PromoteRollout advances only the expected successful cohort. Repeating an old
// promotion cannot accidentally promote a second cohort, even on another replica.
func (api *Api) PromoteRollout(ctx context.Context, token string, expected int) (*UpdateRolloutView, error) {
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
		if r.CurrentCohort != expected || (r.State != RolloutRunning && !(r.State == RolloutPaused && r.PauseReason == PauseCohortReady)) {
			return nil, refuse(CodeRolloutStateConflict, "cohort cannot be promoted in this state")
		}
		ok, err := cohortSucceeded(tx, r)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, refuse(CodeRolloutStateConflict, "every cohort target must confirm success before promotion")
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
		ID      uint
		State   string
		Version int64
	}
	err = tx.Model(&UpdateRolloutTarget{}).Select("update_attempts.id, update_attempts.state, update_attempts.version").
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
			key := fmt.Sprintf("%d:%s:%d", row.ID, row.State, row.Version)
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
