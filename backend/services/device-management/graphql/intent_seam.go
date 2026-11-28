// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-device-management/internal/intentseam"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// intentSeamSubject is the one service subject the observation seam answers.
//
// What the subject is worth depends on who can mint it. Until the area-bound mint ships (the
// automation area's own credential, a subject derived by the issuer rather than named by the
// caller, and an enforced authority ceiling) any holder of the shared service secret can mint a
// token with this subject, so this guard names the intended caller; it is not by itself a
// boundary. The seam is read-only either way.
//
// TODO(IL-S1): take this from the core/auth area-bound caller list (auth.AreaBoundCallers) once it
// is on main, instead of repeating the name.
const intentSeamSubject = "automation"

// intentSeamCaller is the guard every observation-seam query passes before any model call, as
// user-management guards principalStanding: a service token holding device:read (which also
// refuses a person and a scoped caller), whose subject is the automation area, and that is not of
// agent origin. A token with any other subject is ErrForbidden.
func intentSeamCaller(ctx context.Context) error {
	if err := auth.AuthorizeService(ctx, auth.DeviceRead); err != nil {
		return err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Subject != intentSeamSubject || auth.IsAgentOrigin(claims) {
		return auth.ErrForbidden
	}
	return nil
}

// ---- inputs ----

type IntentSeamTargetInput struct {
	GroupToken string
	Version    int32
}

func (in IntentSeamTargetInput) toModel() model.IntentSeamTarget {
	return model.IntentSeamTarget{GroupToken: in.GroupToken, Version: in.Version}
}

// ---- resolvers ----

type ConfigurationKeyDeclarationCountResolver struct{ M intentseam.DeclarationCount }

func (r *ConfigurationKeyDeclarationCountResolver) DeclaredValueType() *string {
	return r.M.DeclaredValueType
}
func (r *ConfigurationKeyDeclarationCountResolver) Count() int32 { return int32(r.M.Count) }

type ConfigurationKeyStateDigestResolver struct{ M intentseam.DigestAnswer }

func (r *ConfigurationKeyStateDigestResolver) Rejected() bool  { return r.M.Rejected }
func (r *ConfigurationKeyStateDigestResolver) Code() *string   { return optional(r.M.Code) }
func (r *ConfigurationKeyStateDigestResolver) Reason() *string { return optional(r.M.Reason) }
func (r *ConfigurationKeyStateDigestResolver) ResolvedVersion() int32 {
	return r.M.ResolvedVersion
}
func (r *ConfigurationKeyStateDigestResolver) MemberCount() int32   { return int32(r.M.MemberCount) }
func (r *ConfigurationKeyStateDigestResolver) Digest() *string      { return optional(r.M.Digest) }
func (r *ConfigurationKeyStateDigestResolver) PresentCount() int32  { return int32(r.M.PresentCount) }
func (r *ConfigurationKeyStateDigestResolver) OversizeCount() int32 { return int32(r.M.OversizeCount) }
func (r *ConfigurationKeyStateDigestResolver) Declarations() []*ConfigurationKeyDeclarationCountResolver {
	out := make([]*ConfigurationKeyDeclarationCountResolver, 0, len(r.M.Declarations))
	for _, d := range r.M.Declarations {
		out = append(out, &ConfigurationKeyDeclarationCountResolver{M: d})
	}
	return out
}

type ConfigurationKeyStateRowResolver struct{ M intentseam.Row }

func (r *ConfigurationKeyStateRowResolver) DeviceId() string {
	return strconv.FormatInt(r.M.DeviceID, 10)
}
func (r *ConfigurationKeyStateRowResolver) Token() string              { return r.M.Token }
func (r *ConfigurationKeyStateRowResolver) DeclaredValueType() *string { return r.M.DeclaredValueType }
func (r *ConfigurationKeyStateRowResolver) Present() bool              { return r.M.Present }
func (r *ConfigurationKeyStateRowResolver) ValueType() *string         { return optional(r.M.ValueType) }
func (r *ConfigurationKeyStateRowResolver) ValueDigest() *string       { return optional(r.M.ValueDigest) }

type ConfigurationKeyStatePageResolver struct{ M intentseam.PageAnswer }

func (r *ConfigurationKeyStatePageResolver) Rejected() bool  { return r.M.Rejected }
func (r *ConfigurationKeyStatePageResolver) Code() *string   { return optional(r.M.Code) }
func (r *ConfigurationKeyStatePageResolver) Reason() *string { return optional(r.M.Reason) }
func (r *ConfigurationKeyStatePageResolver) ResolvedVersion() int32 {
	return r.M.ResolvedVersion
}
func (r *ConfigurationKeyStatePageResolver) Rows() []*ConfigurationKeyStateRowResolver {
	out := make([]*ConfigurationKeyStateRowResolver, 0, len(r.M.Rows))
	for _, row := range r.M.Rows {
		out = append(out, &ConfigurationKeyStateRowResolver{M: row})
	}
	return out
}
func (r *ConfigurationKeyStatePageResolver) NextCursor() *string {
	if r.M.NextCursor == 0 {
		return nil
	}
	c := strconv.FormatInt(r.M.NextCursor, 10)
	return &c
}

type GroupTargetsIntersectionResolver struct{ M intentseam.IntersectAnswer }

func (r *GroupTargetsIntersectionResolver) Rejected() bool  { return r.M.Rejected }
func (r *GroupTargetsIntersectionResolver) Code() *string   { return optional(r.M.Code) }
func (r *GroupTargetsIntersectionResolver) Reason() *string { return optional(r.M.Reason) }
func (r *GroupTargetsIntersectionResolver) Intersects() *bool {
	if r.M.Rejected {
		return nil
	}
	v := r.M.Intersects
	return &v
}

type BulkChangeGranteeStandingResolver struct{ Qualifies_ *bool }

func (r *BulkChangeGranteeStandingResolver) Qualifies() *bool { return r.Qualifies_ }

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- queries ----

func (r *SchemaResolver) ConfigurationKeyStateDigest(ctx context.Context, args struct {
	Target IntentSeamTargetInput
	Key    string
}) (*ConfigurationKeyStateDigestResolver, error) {
	if err := intentSeamCaller(ctx); err != nil {
		return nil, err
	}
	out, err := r.GetApi(ctx).ConfigurationKeyStateDigest(ctx, args.Target.toModel(), args.Key)
	if err != nil {
		return nil, err
	}
	return &ConfigurationKeyStateDigestResolver{M: *out}, nil
}

func (r *SchemaResolver) ConfigurationKeyState(ctx context.Context, args struct {
	Target  IntentSeamTargetInput
	Key     string
	AfterId *string
	Limit   int32
}) (*ConfigurationKeyStatePageResolver, error) {
	if err := intentSeamCaller(ctx); err != nil {
		return nil, err
	}
	if args.Limit < 1 || args.Limit > intentseam.MaxPageRows {
		return nil, fmt.Errorf("limit must be between 1 and %d, got %d", intentseam.MaxPageRows, args.Limit)
	}
	var after int64
	if args.AfterId != nil && *args.AfterId != "" {
		parsed, err := parseCursor(*args.AfterId)
		if err != nil {
			return nil, err
		}
		if parsed > uint64(1<<63-1) {
			return nil, fmt.Errorf("afterId %q is out of range", *args.AfterId)
		}
		after = int64(parsed)
	}
	out, err := r.GetApi(ctx).ConfigurationKeyState(ctx, args.Target.toModel(), args.Key, after, int(args.Limit))
	if err != nil {
		return nil, err
	}
	return &ConfigurationKeyStatePageResolver{M: *out}, nil
}

func (r *SchemaResolver) GroupTargetsIntersect(ctx context.Context, args struct {
	A IntentSeamTargetInput
	B IntentSeamTargetInput
}) (*GroupTargetsIntersectionResolver, error) {
	if err := intentSeamCaller(ctx); err != nil {
		return nil, err
	}
	out, err := r.GetApi(ctx).GroupTargetsIntersect(ctx, args.A.toModel(), args.B.toModel())
	if err != nil {
		return nil, err
	}
	return &GroupTargetsIntersectionResolver{M: *out}, nil
}

func (r *SchemaResolver) PreviewIntentBulkChange(ctx context.Context, args struct {
	Kind         string
	ConfigSet    FleetConfigSetInput
	DeviceTokens []string
}) (*FleetOperationPlanResolver, error) {
	if err := intentSeamCaller(ctx); err != nil {
		return nil, err
	}
	in := FleetOperationCreateRequestInput{Kind: args.Kind, ConfigSet: &args.ConfigSet, DeviceTokens: args.DeviceTokens}
	req := in.toModel()
	plan, err := r.GetApi(ctx).PreviewIntentBulkChange(ctx, req.Kind, req.ConfigSet, req.DeviceTokens)
	if err != nil {
		return nil, err
	}
	return &FleetOperationPlanResolver{M: plan}, nil
}

func (r *SchemaResolver) BulkChangeGranteeStanding(ctx context.Context, args struct {
	Principal string
}) (*BulkChangeGranteeStandingResolver, error) {
	if err := intentSeamCaller(ctx); err != nil {
		return nil, err
	}
	q, err := r.GetApi(ctx).BulkChangeGranteeStanding(ctx, args.Principal)
	if err != nil {
		return nil, err
	}
	return &BulkChangeGranteeStandingResolver{Qualifies_: q}, nil
}
