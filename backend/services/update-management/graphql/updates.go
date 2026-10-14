// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-update-management/model"
	gql "github.com/graph-gophers/graphql-go"
)

// UpdateAssignment looks an update assignment up by token.
func (r *SchemaResolver) UpdateAssignment(ctx context.Context, args struct{ Token string }) (*UpdateAssignmentResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := api.AssignmentByToken(ctx, args.Token)
	if err != nil || v == nil {
		return nil, err
	}
	return &UpdateAssignmentResolver{M: *v}, nil
}

// UpdateAssignments searches update assignments.
func (r *SchemaResolver) UpdateAssignments(ctx context.Context, args struct {
	Criteria struct {
		PageNumber  int32
		PageSize    int32
		DeviceToken *string
		Component   *string
		CurrentOnly *bool
	}
}) (*UpdateAssignmentSearchResultsResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactRead); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	c := args.Criteria
	found, err := api.UpdateAssignments(ctx, model.UpdateAssignmentCriteria{
		Pagination:  rdb.Pagination{PageNumber: c.PageNumber, PageSize: c.PageSize},
		DeviceToken: c.DeviceToken,
		Component:   c.Component,
		CurrentOnly: c.CurrentOnly != nil && *c.CurrentOnly,
	})
	if err != nil {
		return nil, err
	}
	return &UpdateAssignmentSearchResultsResolver{M: *found}, nil
}

// AssignArtifact tells a device to install an artifact.
func (r *SchemaResolver) AssignArtifact(ctx context.Context, args struct {
	DeviceToken   string
	ArtifactToken string
}) (*UpdateAssignmentResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := api.AssignArtifact(ctx, args.DeviceToken, args.ArtifactToken)
	if err != nil {
		return nil, err
	}
	return &UpdateAssignmentResolver{M: *v}, nil
}

// CancelUpdate cancels an update.
func (r *SchemaResolver) CancelUpdate(ctx context.Context, args struct{ AssignmentToken string }) (*UpdateAssignmentResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := api.CancelUpdate(ctx, args.AssignmentToken)
	if err != nil {
		return nil, err
	}
	return &UpdateAssignmentResolver{M: *v}, nil
}

// AbandonUnknownUpdate ends an UNKNOWN update on an operator decision.
func (r *SchemaResolver) AbandonUnknownUpdate(ctx context.Context, args struct {
	AssignmentToken string
	Reason          string
}) (*UpdateAssignmentResolver, error) {
	if err := auth.Authorize(ctx, auth.ArtifactWrite); err != nil {
		return nil, err
	}
	api, err := r.GetApi(ctx)
	if err != nil {
		return nil, err
	}
	v, err := api.AbandonUnknown(ctx, args.AssignmentToken, args.Reason)
	if err != nil {
		return nil, err
	}
	return &UpdateAssignmentResolver{M: *v}, nil
}

// UpdateAssignmentResolver resolves the fields of one update assignment.
type UpdateAssignmentResolver struct{ M model.UpdateAssignmentView }

func (r *UpdateAssignmentResolver) Id() gql.ID { return gql.ID(fmt.Sprint(r.M.Assignment.ID)) }
func (r *UpdateAssignmentResolver) CreatedAt() *string {
	return gqlcore.FormatTime(r.M.Assignment.CreatedAt)
}
func (r *UpdateAssignmentResolver) Token() string { return r.M.Assignment.Token }
func (r *UpdateAssignmentResolver) DeviceToken() string {
	return r.M.Assignment.DeviceToken
}
func (r *UpdateAssignmentResolver) Component() string { return r.M.Assignment.Component }
func (r *UpdateAssignmentResolver) Artifact() *ArtifactResolver {
	return &ArtifactResolver{M: r.M.Artifact}
}
func (r *UpdateAssignmentResolver) Actor() *string {
	if r.M.Assignment.Actor == "" {
		return nil
	}
	return &r.M.Assignment.Actor
}
func (r *UpdateAssignmentResolver) SupersededAt() *string {
	if !r.M.Assignment.SupersededAt.Valid {
		return nil
	}
	return gqlcore.FormatTime(r.M.Assignment.SupersededAt.Time)
}
func (r *UpdateAssignmentResolver) Current() bool { return !r.M.Assignment.SupersededAt.Valid }
func (r *UpdateAssignmentResolver) Attempt() *UpdateAttemptResolver {
	return &UpdateAttemptResolver{M: r.M.Attempt}
}

// SignedBy is the signer behind the assignment's pinned signature, if it pins one.
func (r *UpdateAssignmentResolver) SignedBy() *assignmentSignerResolver {
	if r.M.Pin == nil {
		return nil
	}
	return &assignmentSignerResolver{m: r.M.Pin.Signer}
}

type assignmentSignerResolver struct{ m model.ArtifactSigner }

func (r *assignmentSignerResolver) Token() string     { return r.m.Token }
func (r *assignmentSignerResolver) Name() string      { return r.m.Name }
func (r *assignmentSignerResolver) KeyId() string     { return r.m.KeyId }
func (r *assignmentSignerResolver) Algorithm() string { return r.m.Algorithm }
func (r *assignmentSignerResolver) State() string     { return r.m.State }

// UpdateAttemptResolver resolves the fields of one update attempt.
type UpdateAttemptResolver struct{ M model.UpdateAttempt }

func (r *UpdateAttemptResolver) Token() string         { return r.M.Token }
func (r *UpdateAttemptResolver) State() string         { return r.M.State }
func (r *UpdateAttemptResolver) TargetVersion() string { return r.M.TargetVersion }
func (r *UpdateAttemptResolver) TargetDigest() string  { return r.M.TargetDigest }
func (r *UpdateAttemptResolver) RequiresReboot() bool  { return r.M.RequiresReboot }
func (r *UpdateAttemptResolver) LastReportAt() *string { return gqlcore.FormatTime(r.M.LastReportAt) }
func (r *UpdateAttemptResolver) CancelRequested() bool { return r.M.CancelRequested }
func (r *UpdateAttemptResolver) Reconciled() bool      { return r.M.Reconciled }
func (r *UpdateAttemptResolver) Progress() *updateProgressResolver {
	if !r.M.ProgressBytes.Valid {
		return nil
	}
	return &updateProgressResolver{bytes: r.M.ProgressBytes.Int64, total: r.M.ProgressTotal.Int64}
}
func (r *UpdateAttemptResolver) Failure() *updateFailureResolver {
	if r.M.FailureCode == "" && r.M.FailureStage == "" {
		return nil
	}
	return &updateFailureResolver{m: r.M}
}

type updateProgressResolver struct{ bytes, total int64 }

func (r *updateProgressResolver) Bytes() string { return strconv.FormatInt(r.bytes, 10) }
func (r *updateProgressResolver) Total() string { return strconv.FormatInt(r.total, 10) }

type updateFailureResolver struct{ m model.UpdateAttempt }

func (r *updateFailureResolver) Stage() string  { return r.m.FailureStage }
func (r *updateFailureResolver) Code() string   { return r.m.FailureCode }
func (r *updateFailureResolver) Detail() string { return r.m.FailureDetail }
func (r *updateFailureResolver) Platform() bool { return r.m.FailurePlatform }

// UpdateAssignmentSearchResultsResolver resolves a page of update assignments.
type UpdateAssignmentSearchResultsResolver struct{ M model.UpdateAssignmentResults }

func (r *UpdateAssignmentSearchResultsResolver) Results() []*UpdateAssignmentResolver {
	out := make([]*UpdateAssignmentResolver, 0, len(r.M.Results))
	for _, v := range r.M.Results {
		out = append(out, &UpdateAssignmentResolver{M: v})
	}
	return out
}

func (r *UpdateAssignmentSearchResultsResolver) Pagination() *searchResultsPaginationResolver {
	return &searchResultsPaginationResolver{M: r.M.Pagination}
}
