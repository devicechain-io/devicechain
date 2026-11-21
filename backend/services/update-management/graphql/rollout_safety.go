// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-update-management/model"
)

func (r *SchemaResolver) PromoteUpdateRollout(ctx context.Context, args struct {
	Token          string
	ExpectedCohort int32
}) (*UpdateRolloutResolver, error) {
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) {
		return api.PromoteRollout(ctx, args.Token, int(args.ExpectedCohort))
	})
}

func (r *SchemaResolver) ResumeHaltedUpdateRollout(ctx context.Context, args struct {
	Token            string
	HaltSequence     int32
	FailureThreshold *int32
}) (*UpdateRolloutResolver, error) {
	var threshold *int
	if args.FailureThreshold != nil {
		v := int(*args.FailureThreshold)
		threshold = &v
	}
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) {
		return api.ResumeHaltedRollout(ctx, args.Token, int(args.HaltSequence), threshold)
	})
}

func (r *UpdateRolloutResolver) CurrentCohort() int32 { return int32(r.M.Rollout.CurrentCohort) }
func (r *UpdateRolloutResolver) HaltSequence() int32  { return int32(r.M.Rollout.HaltSequence) }
func (r *UpdateRolloutResolver) HaltFailures() int32  { return int32(r.M.Rollout.HaltFailures) }
func (r *UpdateRolloutResolver) HaltSamples() int32   { return int32(r.M.Rollout.HaltSamples) }

func (r *UpdateRolloutResolver) Cohorts() ([]*cohortResolver, error) {
	plan, err := r.M.Rollout.Cohorts()
	if err != nil {
		return nil, err
	}
	out := make([]*cohortResolver, 0, len(plan))
	for _, c := range plan {
		out = append(out, &cohortResolver{c})
	}
	return out, nil
}

func (r *UpdateRolloutResolver) FailureRate() (*failureRateResolver, error) {
	f, err := r.M.Rollout.FailureRate()
	if err != nil || f == nil {
		return nil, err
	}
	return &failureRateResolver{*f}, nil
}

type cohortResolver struct{ c model.RolloutCohort }

func (r *cohortResolver) Name() string          { return r.c.Name }
func (r *cohortResolver) Percentage() int32     { return r.c.Percentage }
func (r *cohortResolver) ManualPromotion() bool { return r.c.ManualPromotion }
func (r *cohortResolver) DeviceTokens() []string {
	return append([]string{}, r.c.DeviceTokens...)
}

type failureRateResolver struct{ f model.RolloutFailureRate }

func (r *failureRateResolver) Percentage() int32     { return r.f.Percentage }
func (r *failureRateResolver) WindowSeconds() int32  { return r.f.WindowSeconds }
func (r *failureRateResolver) MinimumSamples() int32 { return r.f.MinimumSamples }

func (r *updateRolloutTargetResolver) Cohort() int32 { return int32(r.m.Target.Cohort) }
