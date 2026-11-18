// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
)

func authorizeReviewPolicyRead(ctx context.Context) error {
	if claims, ok := auth.ClaimsFromContext(ctx); ok && len(claims.ScopeAnchors) != 0 {
		return actiongate.Refusal(actiongate.CodeScopedNotSupported, "automation protection reads do not support scoped grants", true)
	}
	return auth.Authorize(ctx, auth.DeviceRead)
}

func (r *SchemaResolver) AutomationReviewPolicy(ctx context.Context, args struct{ ProfileToken string }) (*AutomationReviewPolicyResolver, error) {
	if err := authorizeReviewPolicyRead(ctx); err != nil {
		return nil, err
	}
	view, err := r.GetApi(ctx).AutomationReviewPolicyFor(ctx, args.ProfileToken)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewPolicyResolver{M: view, profileToken: args.ProfileToken}, nil
}

func (r *SchemaResolver) AutomationReviewBreakGlass(ctx context.Context, args struct{ ProfileToken string }) (*[]*AutomationReviewBreakGlassResolver, error) {
	if err := authorizeReviewPolicyRead(ctx); err != nil {
		return nil, err
	}
	rows, err := r.GetApi(ctx).AutomationReviewBreakGlassFor(ctx, args.ProfileToken)
	if err != nil {
		return nil, err
	}
	out := make([]*AutomationReviewBreakGlassResolver, 0, len(rows))
	for _, row := range rows {
		out = append(out, &AutomationReviewBreakGlassResolver{M: row})
	}
	return &out, nil
}

type AutomationReviewPolicyInput struct {
	Required            bool
	RequiredApprovals   *int32
	AllowSelfApproval   *bool
	ApprovalTtlSeconds  *int32
	StepUpMaxAgeSeconds *int32
}

func (in AutomationReviewPolicyInput) settings() model.ReviewPolicySettings {
	v := model.DefaultReviewPolicySettings()
	v.Required = in.Required
	if in.RequiredApprovals != nil {
		v.RequiredApprovals = int(*in.RequiredApprovals)
	}
	if in.AllowSelfApproval != nil {
		v.AllowSelfApproval = *in.AllowSelfApproval
	}
	if in.ApprovalTtlSeconds != nil {
		v.ApprovalTtlSeconds = int(*in.ApprovalTtlSeconds)
	}
	if in.StepUpMaxAgeSeconds != nil {
		v.StepUpMaxAgeSeconds = int(*in.StepUpMaxAgeSeconds)
	}
	return v
}

func (r *SchemaResolver) SetAutomationReviewPolicy(ctx context.Context, args struct {
	ProfileToken string
	Settings     AutomationReviewPolicyInput
}) (*SetAutomationReviewPolicyResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	view, applied, err := r.GetApi(ctx).SetAutomationReviewPolicy(ctx, args.ProfileToken, args.Settings.settings())
	if err != nil {
		return nil, err
	}
	return &SetAutomationReviewPolicyResultResolver{applied: applied, policy: &AutomationReviewPolicyResolver{M: view, profileToken: args.ProfileToken}}, nil
}

func (r *SchemaResolver) ApproveAutomationReviewPolicyChange(ctx context.Context, args struct{ Token string }) (*AutomationReviewPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	view, err := r.GetApi(ctx).ApproveAutomationReviewPolicyChange(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return r.reviewPolicyResolver(ctx, view)
}

func (r *SchemaResolver) CancelAutomationReviewPolicyChange(ctx context.Context, args struct{ Token string }) (*AutomationReviewPolicyResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	view, err := r.GetApi(ctx).CancelAutomationReviewPolicyChange(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return r.reviewPolicyResolver(ctx, view)
}

type SetAutomationReviewPolicyResultResolver struct {
	applied bool
	policy  *AutomationReviewPolicyResolver
}

func (r *SetAutomationReviewPolicyResultResolver) Applied() bool { return r.applied }
func (r *SetAutomationReviewPolicyResultResolver) Policy() *AutomationReviewPolicyResolver {
	return r.policy
}

type AutomationReviewPolicyResolver struct {
	M            *model.AutomationReviewPolicyView
	profileToken string
}

// Resolve the token before returning the nullable mutation result so the policy's
// non-null profileToken field is a plain value, with no fallible field resolver.
func (r *SchemaResolver) reviewPolicyResolver(ctx context.Context, view *model.AutomationReviewPolicyView) (*AutomationReviewPolicyResolver, error) {
	var profile model.DeviceProfile
	if err := r.GetApi(ctx).RDB.DB(ctx).First(&profile, view.Stored.DeviceProfileId).Error; err != nil {
		return nil, err
	}
	return &AutomationReviewPolicyResolver{M: view, profileToken: profile.Token}, nil
}
func (r *AutomationReviewPolicyResolver) ProfileToken() string { return r.profileToken }
func (r *AutomationReviewPolicyResolver) Effective() *AutomationReviewPolicySettingsResolver {
	return &AutomationReviewPolicySettingsResolver{M: r.M.Effective}
}
func (r *AutomationReviewPolicyResolver) Revision() int32 {
	if r.M.Stored == nil {
		return 0
	}
	return r.M.Stored.Revision
}
func (r *AutomationReviewPolicyResolver) UpdatedBy() *string {
	if r.M.Stored == nil {
		return nil
	}
	return &r.M.Stored.UpdatedBy
}
func (r *AutomationReviewPolicyResolver) UpdatedAt() *string {
	if r.M.Stored == nil {
		return nil
	}
	v := rfc3339(r.M.Stored.UpdatedAt)
	return &v
}
func (r *AutomationReviewPolicyResolver) PendingChange() *AutomationReviewPolicyChangeResolver {
	if r.M.Pending == nil {
		return nil
	}
	return &AutomationReviewPolicyChangeResolver{M: r.M.Pending}
}

type AutomationReviewPolicySettingsResolver struct{ M model.ReviewPolicySettings }

func (r *AutomationReviewPolicySettingsResolver) Required() bool { return r.M.Required }
func (r *AutomationReviewPolicySettingsResolver) RequiredApprovals() int32 {
	return int32(r.M.RequiredApprovals)
}
func (r *AutomationReviewPolicySettingsResolver) AllowSelfApproval() bool {
	return r.M.AllowSelfApproval
}
func (r *AutomationReviewPolicySettingsResolver) ApprovalTtlSeconds() int32 {
	return int32(r.M.ApprovalTtlSeconds)
}
func (r *AutomationReviewPolicySettingsResolver) StepUpMaxAgeSeconds() int32 {
	return int32(r.M.StepUpMaxAgeSeconds)
}

type AutomationReviewPolicyChangeResolver struct {
	M *model.AutomationReviewPolicyChange
}

func (r *AutomationReviewPolicyChangeResolver) Token() string { return r.M.Token }
func (r *AutomationReviewPolicyChangeResolver) Proposed() (*AutomationReviewPolicySettingsResolver, error) {
	// Stored proposals are complete settings, unlike the partial GraphQL input.
	// Missing or null members must not silently become plausible zero values.
	var members map[string]json.RawMessage
	if err := reviewObject(r.M.Proposed, &members); err != nil {
		return nil, err
	}
	for _, key := range []string{"required", "requiredApprovals", "allowSelfApproval", "approvalTtlSeconds", "stepUpMaxAgeSeconds"} {
		if raw, ok := members[key]; !ok || string(raw) == "null" {
			return nil, fmt.Errorf("stored review policy settings are missing %s", key)
		}
	}
	var v model.ReviewPolicySettings
	if err := reviewObject(r.M.Proposed, &v); err != nil {
		return nil, err
	}
	return &AutomationReviewPolicySettingsResolver{M: v}, nil
}
func (r *AutomationReviewPolicyChangeResolver) ProposedBy() string { return r.M.ProposedBy }
func (r *AutomationReviewPolicyChangeResolver) ProposedAt() string { return rfc3339(r.M.CreatedAt) }
func (r *AutomationReviewPolicyChangeResolver) EffectiveAt() *string {
	if !r.M.EffectiveAt.Valid {
		return nil
	}
	v := rfc3339(r.M.EffectiveAt.Time)
	return &v
}
func (r *AutomationReviewPolicyChangeResolver) State() string { return r.M.State }
func (r *AutomationReviewPolicyChangeResolver) DecidedBy() *string {
	if !r.M.DecidedBy.Valid {
		return nil
	}
	return &r.M.DecidedBy.String
}
func (r *AutomationReviewPolicyChangeResolver) DecidedAt() *string {
	if !r.M.DecidedAt.Valid {
		return nil
	}
	v := rfc3339(r.M.DecidedAt.Time)
	return &v
}

type AutomationReviewBreakGlassResolver struct {
	M model.AutomationReviewBreakGlass
}

func (r *AutomationReviewBreakGlassResolver) Action() string { return r.M.Action }
func (r *AutomationReviewBreakGlassResolver) Target() string { return r.M.Target }
func (r *AutomationReviewBreakGlassResolver) Reason() string { return r.M.Reason }
func (r *AutomationReviewBreakGlassResolver) Actor() string  { return r.M.Actor }
func (r *AutomationReviewBreakGlassResolver) At() string     { return rfc3339(r.M.CreatedAt) }
