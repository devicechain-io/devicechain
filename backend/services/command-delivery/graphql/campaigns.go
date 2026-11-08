// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-command-delivery/model"
)

// authorizeCampaigns is the gate on every campaign operation. A campaign names a group and
// its tracking lists the group's devices, so reading one is reading group membership, which is
// device-management's data: the caller needs device:read as well as the command authority,
// for the reason createCommandBatch gives. Authorize also refuses a caller limited to part of
// the tenant, which a fleet-wide schedule has no meaning for.
func authorizeCampaigns(ctx context.Context, write bool) error {
	need := auth.CommandRead
	if write {
		need = auth.CommandWrite
	}
	if err := auth.Authorize(ctx, need); err != nil {
		return err
	}
	return auth.Authorize(ctx, auth.DeviceRead)
}

// CampaignResolver resolves a Campaign.
type CampaignResolver struct {
	M model.Campaign
}

func (r *CampaignResolver) Id() gql.ID                { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *CampaignResolver) CreatedAt() *string        { return util.FormatTime(r.M.CreatedAt) }
func (r *CampaignResolver) UpdatedAt() *string        { return util.FormatTime(r.M.UpdatedAt) }
func (r *CampaignResolver) Token() string             { return r.M.Token }
func (r *CampaignResolver) Enabled() bool             { return r.M.Enabled }
func (r *CampaignResolver) Trigger() string           { return r.M.Trigger.String() }
func (r *CampaignResolver) TargetKind() string        { return r.M.TargetKind }
func (r *CampaignResolver) GroupToken() string        { return r.M.GroupToken }
func (r *CampaignResolver) MaxLatenessSeconds() int32 { return int32(r.M.MaxLatenessSeconds) }
func (r *CampaignResolver) ExpiresInSeconds() int32   { return int32(r.M.ExpiresInSeconds) }

func (r *CampaignResolver) Name() *string {
	if r.M.Name == "" {
		return nil
	}
	return &r.M.Name
}

func (r *CampaignResolver) PlannedThrough() *string {
	if !r.M.PlannedThrough.Valid {
		return nil
	}
	return util.FormatTime(r.M.PlannedThrough.Time)
}

func (r *CampaignResolver) LastError() *string {
	if !r.M.LastError.Valid {
		return nil
	}
	return &r.M.LastError.String
}

// Steps fails rather than answering an empty list for a stored step list it cannot read: an
// empty list is a plausible answer (a campaign that sends nothing) and would hide the fault.
func (r *CampaignResolver) Steps() ([]*CampaignStepResolver, error) {
	steps, err := model.DecodeSteps(r.M.Steps)
	if err != nil {
		return nil, fmt.Errorf("campaign %q has an unreadable step list", r.M.Token)
	}
	out := make([]*CampaignStepResolver, 0, len(steps))
	for _, s := range steps {
		out = append(out, &CampaignStepResolver{M: s})
	}
	return out, nil
}

// CampaignStepResolver resolves a CampaignStep.
type CampaignStepResolver struct{ M model.CampaignStep }

func (r *CampaignStepResolver) Type() string { return r.M.Type }
func (r *CampaignStepResolver) Name() *string {
	if r.M.Name == "" {
		return nil
	}
	return &r.M.Name
}
func (r *CampaignStepResolver) Payload() *string {
	if r.M.Payload == "" {
		return nil
	}
	return &r.M.Payload
}
func (r *CampaignStepResolver) DelaySeconds() int32 { return int32(r.M.DelaySeconds) }

// CreateCampaignResultResolver carries EITHER the campaign OR the rejection that refused it.
type CreateCampaignResultResolver struct {
	campaign  *CampaignResolver
	rejection *CommandBatchRejectionResolver
}

func (r *CreateCampaignResultResolver) Campaign() *CampaignResolver { return r.campaign }
func (r *CreateCampaignResultResolver) Rejection() *CommandBatchRejectionResolver {
	return r.rejection
}

// CampaignCycleResolver resolves a CampaignCycle.
type CampaignCycleResolver struct {
	M             model.CampaignCycle
	CampaignToken string
	S             *SchemaResolver
	C             context.Context
}

func (r *CampaignCycleResolver) Key() string        { return r.M.CycleKey }
func (r *CampaignCycleResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *CampaignCycleResolver) Resolved() int32    { return int32(r.M.Resolved) }

func (r *CampaignCycleResolver) Day() *string {
	if !r.M.Day.Valid {
		return nil
	}
	return &r.M.Day.String
}

func (r *CampaignCycleResolver) At() *string {
	if !r.M.At.Valid {
		return nil
	}
	return util.FormatTime(r.M.At.Time)
}

func (r *CampaignCycleResolver) Counts() (*CampaignCycleCountsResolver, error) {
	counts, err := r.S.GetApi(r.C).CampaignCycleSummary(r.C, r.CampaignToken, r.M.CycleKey)
	if err != nil {
		return nil, err
	}
	return &CampaignCycleCountsResolver{M: *counts}, nil
}

// CampaignCycleCountsResolver resolves CampaignCycleCounts.
type CampaignCycleCountsResolver struct{ M model.CampaignCycleCounts }

func (r *CampaignCycleCountsResolver) Pending() int32    { return int32(r.M.Pending) }
func (r *CampaignCycleCountsResolver) Claimed() int32    { return int32(r.M.Claimed) }
func (r *CampaignCycleCountsResolver) Dispatched() int32 { return int32(r.M.Dispatched) }
func (r *CampaignCycleCountsResolver) Refused() int32    { return int32(r.M.Refused) }
func (r *CampaignCycleCountsResolver) Skipped() int32    { return int32(r.M.Skipped) }
func (r *CampaignCycleCountsResolver) Succeeded() int32  { return int32(r.M.Succeeded) }
func (r *CampaignCycleCountsResolver) InFlight() int32   { return int32(r.M.InFlight) }
func (r *CampaignCycleCountsResolver) Waiting() int32    { return int32(r.M.Waiting) }
func (r *CampaignCycleCountsResolver) Failed() int32     { return int32(r.M.Failed) }
func (r *CampaignCycleCountsResolver) Unknown() int32    { return int32(r.M.Unknown) }

// CampaignElementResolver resolves a CampaignElement.
type CampaignElementResolver struct{ M *model.CampaignElementView }

func (r *CampaignElementResolver) Id() gql.ID          { return gql.ID(fmt.Sprint(r.M.Element.ID)) }
func (r *CampaignElementResolver) DeviceToken() string { return r.M.Element.DeviceToken }
func (r *CampaignElementResolver) Step() int32         { return int32(r.M.Element.Step) }
func (r *CampaignElementResolver) Status() string      { return r.M.Element.Status }

func (r *CampaignElementResolver) DueAt() string {
	if s := util.FormatTime(r.M.Element.DueAt); s != nil {
		return *s
	}
	return ""
}

func strIf(valid bool, s string) *string {
	if !valid {
		return nil
	}
	return &s
}

func (r *CampaignElementResolver) Reason() *string {
	return strIf(r.M.Element.Reason.Valid, r.M.Element.Reason.String)
}
func (r *CampaignElementResolver) BatchToken() *string {
	return strIf(r.M.Element.BatchToken.Valid, r.M.Element.BatchToken.String)
}
func (r *CampaignElementResolver) RetryBatchToken() *string {
	return strIf(r.M.Element.RetryBatchToken.Valid, r.M.Element.RetryBatchToken.String)
}
func (r *CampaignElementResolver) CommandStatus() *string {
	return strIf(r.M.CommandStatus != "", r.M.CommandStatus)
}
func (r *CampaignElementResolver) CommandOutcome() *string {
	return strIf(r.M.CommandOutcome != "", r.M.CommandOutcome)
}

// CampaignRetryResultResolver resolves CampaignRetryResult.
type CampaignRetryResultResolver struct{ M *model.CampaignRetryResult }

func (r *CampaignRetryResultResolver) Batches() []string {
	if r.M.Batches == nil {
		return []string{}
	}
	return r.M.Batches
}
func (r *CampaignRetryResultResolver) Retried() int32 { return int32(r.M.Retried) }
func (r *CampaignRetryResultResolver) Held() int32    { return int32(r.M.Held) }

// Campaign returns one campaign, or null.
func (r *SchemaResolver) Campaign(ctx context.Context, args struct{ Token string }) (*CampaignResolver, error) {
	if err := authorizeCampaigns(ctx, false); err != nil {
		return nil, err
	}
	found, err := r.GetApi(ctx).CampaignsByToken(ctx, []string{args.Token})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &CampaignResolver{M: *found[0]}, nil
}

// Campaigns lists campaigns.
func (r *SchemaResolver) Campaigns(ctx context.Context, args struct{ Limit *int32 }) ([]*CampaignResolver, error) {
	if err := authorizeCampaigns(ctx, false); err != nil {
		return nil, err
	}
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	found, err := r.GetApi(ctx).ListCampaigns(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*CampaignResolver, 0, len(found))
	for _, c := range found {
		out = append(out, &CampaignResolver{M: *c})
	}
	return out, nil
}

// CampaignCycles lists a campaign's cycles.
func (r *SchemaResolver) CampaignCycles(ctx context.Context, args struct {
	CampaignToken string
	Limit         *int32
}) ([]*CampaignCycleResolver, error) {
	if err := authorizeCampaigns(ctx, false); err != nil {
		return nil, err
	}
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	found, err := r.GetApi(ctx).CampaignCycles(ctx, args.CampaignToken, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*CampaignCycleResolver, 0, len(found))
	for _, cy := range found {
		out = append(out, &CampaignCycleResolver{M: *cy, CampaignToken: args.CampaignToken, S: r, C: ctx})
	}
	return out, nil
}

// CampaignElements pages through a cycle's elements.
func (r *SchemaResolver) CampaignElements(ctx context.Context, args struct {
	CampaignToken string
	CycleKey      string
	Status        *string
	AfterId       *gql.ID
	Limit         *int32
}) ([]*CampaignElementResolver, error) {
	if err := authorizeCampaigns(ctx, false); err != nil {
		return nil, err
	}
	var after uint
	if args.AfterId != nil && *args.AfterId != "" {
		n, err := strconv.ParseUint(string(*args.AfterId), 10, strconv.IntSize)
		if err != nil {
			return nil, fmt.Errorf("afterId %q is not a numeric id", string(*args.AfterId))
		}
		after = uint(n)
	}
	status := ""
	if args.Status != nil {
		status = *args.Status
	}
	limit := 0
	if args.Limit != nil {
		limit = int(*args.Limit)
	}
	found, err := r.GetApi(ctx).CampaignElements(ctx, args.CampaignToken, args.CycleKey, status, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*CampaignElementResolver, 0, len(found))
	for _, v := range found {
		out = append(out, &CampaignElementResolver{M: v})
	}
	return out, nil
}

// CreateCampaign defines a campaign.
func (r *SchemaResolver) CreateCampaign(ctx context.Context, args struct {
	Request model.CampaignCreateRequest
}) (*CreateCampaignResultResolver, error) {
	if err := authorizeCampaigns(ctx, true); err != nil {
		return nil, err
	}
	created, err := r.GetApi(ctx).CreateCampaign(ctx, &args.Request)
	if err != nil {
		var rej *model.EnqueueRejected
		if errors.As(err, &rej) {
			return &CreateCampaignResultResolver{rejection: &CommandBatchRejectionResolver{
				code: string(rej.Code), reason: rej.Reason,
			}}, nil
		}
		return nil, err
	}
	return &CreateCampaignResultResolver{campaign: &CampaignResolver{M: *created}}, nil
}

// SetCampaignEnabled switches a campaign on or off.
func (r *SchemaResolver) SetCampaignEnabled(ctx context.Context, args struct {
	Token   string
	Enabled bool
}) (*CampaignResolver, error) {
	if err := authorizeCampaigns(ctx, true); err != nil {
		return nil, err
	}
	c, err := r.GetApi(ctx).SetCampaignEnabled(ctx, args.Token, args.Enabled)
	if err != nil {
		return nil, err
	}
	return &CampaignResolver{M: *c}, nil
}

// DeleteCampaign removes a campaign.
func (r *SchemaResolver) DeleteCampaign(ctx context.Context, args struct{ Token string }) (bool, error) {
	if err := authorizeCampaigns(ctx, true); err != nil {
		return false, err
	}
	if err := r.GetApi(ctx).DeleteCampaign(ctx, args.Token); err != nil {
		return false, err
	}
	return true, nil
}

// RetryCampaignFailed re-sends a cycle's unsuccessful elements.
func (r *SchemaResolver) RetryCampaignFailed(ctx context.Context, args struct {
	CampaignToken  string
	CycleKey       string
	IncludeUnknown bool
}) (*CampaignRetryResultResolver, error) {
	if err := authorizeCampaigns(ctx, true); err != nil {
		return nil, err
	}
	res, err := r.GetApi(ctx).RetryCampaignFailed(ctx, args.CampaignToken, args.CycleKey, args.IncludeUnknown)
	if err != nil {
		return nil, err
	}
	return &CampaignRetryResultResolver{M: res}, nil
}
