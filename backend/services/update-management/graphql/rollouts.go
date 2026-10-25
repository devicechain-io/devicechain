// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// Every rollout door uses auth.Authorize, which refuses a member limited to part of the tenant
// (ADR-081): a rollout spans a group of devices, most of which are outside any one member's scope,
// so neither its progress nor its controls are a scoped member's business.

// UpdateRollout looks a rollout up by token.
func (r *SchemaResolver) UpdateRollout(ctx context.Context, args struct{ Token string }) (*UpdateRolloutResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := api.RolloutByToken(ctx, args.Token)
	if err != nil || v == nil {
		return nil, err
	}
	return &UpdateRolloutResolver{api: api, M: *v}, nil
}

// UpdateRollouts searches rollouts.
func (r *SchemaResolver) UpdateRollouts(ctx context.Context, args struct {
	Criteria struct {
		PageNumber int32
		PageSize   int32
		State      *string
	}
}) (*UpdateRolloutSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	found, err := api.Rollouts(ctx, model.RolloutSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		State:      args.Criteria.State,
	})
	if err != nil {
		return nil, err
	}
	return &UpdateRolloutSearchResultsResolver{api: api, M: *found}, nil
}

// UpdateRolloutTargets pages a rollout's targets.
func (r *SchemaResolver) UpdateRolloutTargets(ctx context.Context, args struct {
	Token    string
	Criteria struct {
		PageNumber int32
		PageSize   int32
		Outcome    *string
	}
}) (*UpdateRolloutTargetSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	found, err := api.RolloutTargets(ctx, args.Token, model.RolloutTargetCriteria{
		Pagination: rdb.Pagination{PageNumber: args.Criteria.PageNumber, PageSize: args.Criteria.PageSize},
		Outcome:    args.Criteria.Outcome,
	})
	if err != nil || found == nil {
		return nil, err
	}
	return &UpdateRolloutTargetSearchResultsResolver{M: *found}, nil
}

type rolloutCreateRequest struct {
	Token            string
	Name             *string
	ArtifactToken    string
	GroupToken       string
	MaxInFlight      int32
	FailureThreshold *int32
}

// CreateUpdateRollout declares a rollout.
func (r *SchemaResolver) CreateUpdateRollout(ctx context.Context, args struct{ Request rolloutCreateRequest }) (*UpdateRolloutResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	req := args.Request
	in := model.RolloutCreateRequest{Token: req.Token, ArtifactToken: req.ArtifactToken, GroupToken: req.GroupToken,
		MaxInFlight: int(req.MaxInFlight)}
	if req.Name != nil {
		in.Name = *req.Name
	}
	if req.FailureThreshold != nil {
		t := int(*req.FailureThreshold)
		in.FailureThreshold = &t
	}
	v, err := api.CreateRollout(ctx, in)
	if err != nil {
		return nil, err
	}
	return &UpdateRolloutResolver{api: api, M: *v}, nil
}

// rolloutWrite runs one of the rollout controls under artifact:write.
func (r *SchemaResolver) rolloutWrite(ctx context.Context,
	do func(api *model.Api) (*model.UpdateRolloutView, error)) (*UpdateRolloutResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := do(api)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("the rollout could not be read back")
	}
	return &UpdateRolloutResolver{api: api, M: *v}, nil
}

// StartUpdateRollout starts a DRAFT rollout.
func (r *SchemaResolver) StartUpdateRollout(ctx context.Context, args struct{ Token string }) (*UpdateRolloutResolver, error) {
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) { return api.StartRollout(ctx, args.Token) })
}

// PauseUpdateRollout pauses a RUNNING rollout.
func (r *SchemaResolver) PauseUpdateRollout(ctx context.Context, args struct{ Token string }) (*UpdateRolloutResolver, error) {
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) { return api.PauseRollout(ctx, args.Token) })
}

// ResumeUpdateRollout resumes a PAUSED rollout.
func (r *SchemaResolver) ResumeUpdateRollout(ctx context.Context, args struct {
	Token            string
	FailureThreshold *int32
}) (*UpdateRolloutResolver, error) {
	var threshold *int
	if args.FailureThreshold != nil {
		t := int(*args.FailureThreshold)
		threshold = &t
	}
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) {
		return api.ResumeRollout(ctx, args.Token, threshold)
	})
}

// AbortUpdateRollout aborts a rollout.
func (r *SchemaResolver) AbortUpdateRollout(ctx context.Context, args struct{ Token string }) (*UpdateRolloutResolver, error) {
	return r.rolloutWrite(ctx, func(api *model.Api) (*model.UpdateRolloutView, error) { return api.AbortRollout(ctx, args.Token) })
}

// UpdateRolloutResolver resolves one rollout.
type UpdateRolloutResolver struct {
	api *model.Api
	M   model.UpdateRolloutView
}

func (r *UpdateRolloutResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.Rollout.ID)) }
func (r *UpdateRolloutResolver) Token() string      { return r.M.Rollout.Token }
func (r *UpdateRolloutResolver) Name() string       { return r.M.Rollout.Name }
func (r *UpdateRolloutResolver) GroupToken() string { return r.M.Rollout.GroupToken }
func (r *UpdateRolloutResolver) Artifact() *ArtifactResolver {
	return newArtifactResolver(r.api, r.M.Artifact)
}
func (r *UpdateRolloutResolver) GroupVersion() *int32 {
	if !r.M.Rollout.GroupVersion.Valid {
		return nil
	}
	v := r.M.Rollout.GroupVersion.Int32
	return &v
}
func (r *UpdateRolloutResolver) MaxInFlight() int32 { return int32(r.M.Rollout.MaxInFlight) }
func (r *UpdateRolloutResolver) FailureThreshold() *int32 {
	if !r.M.Rollout.FailureThreshold.Valid {
		return nil
	}
	v := r.M.Rollout.FailureThreshold.Int32
	return &v
}
func (r *UpdateRolloutResolver) State() string { return r.M.Rollout.State }
func (r *UpdateRolloutResolver) PauseReason() *string {
	if r.M.Rollout.State != model.RolloutPaused || r.M.Rollout.PauseReason == "" {
		return nil
	}
	return &r.M.Rollout.PauseReason
}
func (r *UpdateRolloutResolver) TargetCount() int32 { return int32(r.M.Rollout.TargetCount) }
func (r *UpdateRolloutResolver) CreatedAt() *string { return gqlcore.FormatTime(r.M.Rollout.CreatedAt) }
func (r *UpdateRolloutResolver) CreatedBy() *string { return nonEmpty(r.M.Rollout.CreatedBy) }
func (r *UpdateRolloutResolver) StartedAt() *string {
	if !r.M.Rollout.StartedAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.M.Rollout.StartedAt.Time)
}
func (r *UpdateRolloutResolver) FinishedAt() *string {
	if !r.M.Rollout.FinishedAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.M.Rollout.FinishedAt.Time)
}
func (r *UpdateRolloutResolver) StateChangedBy() *string { return nonEmpty(r.M.Rollout.StateChangedBy) }
func (r *UpdateRolloutResolver) StateChangedAt() *string {
	if !r.M.Rollout.StateChangedAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.M.Rollout.StateChangedAt.Time)
}
func (r *UpdateRolloutResolver) Progress() *rolloutProgressResolver {
	return &rolloutProgressResolver{m: r.M.Progress}
}

type rolloutProgressResolver struct{ m model.RolloutProgress }

func (r *rolloutProgressResolver) Total() int32     { return int32(r.m.Total) }
func (r *rolloutProgressResolver) Pending() int32   { return int32(r.m.Pending) }
func (r *rolloutProgressResolver) InFlight() int32  { return int32(r.m.InFlight) }
func (r *rolloutProgressResolver) Succeeded() int32 { return int32(r.m.Succeeded) }
func (r *rolloutProgressResolver) Failed() int32    { return int32(r.m.Failed) }
func (r *rolloutProgressResolver) Unknown() int32   { return int32(r.m.Unknown) }
func (r *rolloutProgressResolver) Cancelled() int32 { return int32(r.m.Cancelled) }
func (r *rolloutProgressResolver) Skipped() int32   { return int32(r.m.Skipped) }

// UpdateRolloutSearchResultsResolver resolves a page of rollouts.
type UpdateRolloutSearchResultsResolver struct {
	api *model.Api
	M   model.RolloutSearchResults
}

func (r *UpdateRolloutSearchResultsResolver) Results() []*UpdateRolloutResolver {
	out := make([]*UpdateRolloutResolver, 0, len(r.M.Results))
	for _, v := range r.M.Results {
		out = append(out, &UpdateRolloutResolver{api: r.api, M: v})
	}
	return out
}

func (r *UpdateRolloutSearchResultsResolver) Pagination() *searchResultsPaginationResolver {
	return &searchResultsPaginationResolver{M: r.M.Pagination}
}

// updateRolloutTargetResolver resolves one target.
type updateRolloutTargetResolver struct{ m model.RolloutTargetView }

func (r *updateRolloutTargetResolver) DeviceToken() string      { return r.m.Target.DeviceToken }
func (r *updateRolloutTargetResolver) Outcome() string          { return r.m.Outcome }
func (r *updateRolloutTargetResolver) AssignmentToken() *string { return nonEmpty(r.m.AssignmentToken) }
func (r *updateRolloutTargetResolver) AttemptState() *string    { return nonEmpty(r.m.AttemptState) }
func (r *updateRolloutTargetResolver) SkipCode() *string        { return nonEmpty(r.m.Target.SkipCode) }
func (r *updateRolloutTargetResolver) SkipDetail() *string      { return nonEmpty(r.m.Target.SkipDetail) }
func (r *updateRolloutTargetResolver) DispatchedAt() *string {
	if !r.m.Target.DispatchedAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.m.Target.DispatchedAt.Time)
}

// UpdateRolloutTargetSearchResultsResolver resolves a page of targets.
type UpdateRolloutTargetSearchResultsResolver struct{ M model.RolloutTargetResults }

func (r *UpdateRolloutTargetSearchResultsResolver) Results() []*updateRolloutTargetResolver {
	out := make([]*updateRolloutTargetResolver, 0, len(r.M.Results))
	for _, v := range r.M.Results {
		out = append(out, &updateRolloutTargetResolver{m: v})
	}
	return out
}

func (r *UpdateRolloutTargetSearchResultsResolver) Pagination() *searchResultsPaginationResolver {
	return &searchResultsPaginationResolver{M: r.M.Pagination}
}
