// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	gql "github.com/graph-gophers/graphql-go"
)

// fleetCaller is the guard every fleet-operation query and mutation passes, in this order, before
// any model call:
//  1. an agent-origin token, and a service token, are refused (AGENT_NOT_PERMITTED): the caller
//     must be a person acting under their own access token;
//  2. each required authority is checked, which also refuses a scoped member (auth.Authorize).
//
// It returns the person's sign-in name, which is what the operation's initiator is recorded as.
func fleetCaller(ctx context.Context, required ...auth.Authority) (string, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims == nil {
		return "", auth.ErrUnauthenticated
	}
	if _, scoped := auth.ScopeFromContext(ctx); scoped {
		return "", auth.ErrScopeUnsupported
	}
	if auth.IsAgentOrigin(claims) || claims.TokenType == auth.TokenTypeService {
		return "", &model.FleetOperationError{Code: model.FleetCodeAgentNotPermitted,
			Message: "bulk changes are run by a person acting under their own access token"}
	}
	for _, a := range required {
		if err := auth.Authorize(ctx, a); err != nil {
			return "", err
		}
	}
	return claims.Username, nil
}

// The reads need only device:read; plan and create resolve tokens and read current values on the
// same path as the mutation, so they need device:write and fleet:operate.
var (
	fleetReadAuthorities  = []auth.Authority{auth.DeviceRead}
	fleetWriteAuthorities = []auth.Authority{auth.DeviceWrite, auth.FleetOperate}
)

// ---- inputs ----

type FleetConfigSetInput struct {
	Key       string
	Value     *string
	ValueType *string
	Unset     *bool
}

type FleetOperationCreateSafetyInput struct {
	MaxPostsPerSecond     *int32
	FailureThreshold      *int32
	AmbiguityThreshold    *int32
	SettleDeadlineSeconds *int32
}

type FleetOperationCreateRequestInput struct {
	Token        string
	Name         string
	Kind         string
	ConfigSet    *FleetConfigSetInput
	DeviceTokens []string
	Safety       *FleetOperationCreateSafetyInput
}

func (in FleetOperationCreateRequestInput) toModel() *model.FleetOperationCreateRequest {
	req := &model.FleetOperationCreateRequest{Token: in.Token, Name: in.Name, Kind: in.Kind, DeviceTokens: in.DeviceTokens}
	if in.ConfigSet != nil {
		cs := &model.FleetConfigSetIntent{Key: in.ConfigSet.Key, Value: in.ConfigSet.Value}
		if in.ConfigSet.ValueType != nil {
			cs.ValueType = *in.ConfigSet.ValueType
		}
		if in.ConfigSet.Unset != nil {
			cs.Unset = *in.ConfigSet.Unset
		}
		req.ConfigSet = cs
	}
	if in.Safety != nil {
		req.Safety = model.FleetSafetyRequest{MaxPostsPerSecond: in.Safety.MaxPostsPerSecond, FailureThreshold: in.Safety.FailureThreshold,
			AmbiguityThreshold: in.Safety.AmbiguityThreshold, SettleDeadlineSeconds: in.Safety.SettleDeadlineSeconds}
	}
	return req
}

type FleetOperationSearchCriteriaInput struct {
	rdb.Pagination
	Dispatch *string
	Evidence *string
	Kind     *string
}

type FleetLineSearchCriteriaInput struct {
	rdb.Pagination
	Posting    *string
	Settlement *string
	Code       *string
}

// ---- queries and mutations ----

func (r *SchemaResolver) BulkChange(ctx context.Context, args struct{ Token string }) (*FleetOperationResolver, error) {
	if _, err := fleetCaller(ctx, fleetReadAuthorities...); err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).FleetOperationByToken(ctx, args.Token)
	if err != nil || op == nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

func (r *SchemaResolver) BulkChanges(ctx context.Context, args struct {
	Criteria FleetOperationSearchCriteriaInput
}) (*FleetOperationSearchResultsResolver, error) {
	if _, err := fleetCaller(ctx, fleetReadAuthorities...); err != nil {
		return nil, err
	}
	c := args.Criteria
	results, err := r.GetApi(ctx).FleetOperations(ctx, model.FleetOperationSearchCriteria{Pagination: c.Pagination, Dispatch: c.Dispatch, Evidence: c.Evidence, Kind: c.Kind})
	if err != nil {
		return nil, err
	}
	return &FleetOperationSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}

func (r *SchemaResolver) BulkChangeLines(ctx context.Context, args struct {
	Token    string
	Criteria FleetLineSearchCriteriaInput
}) (*FleetLineSearchResultsResolver, error) {
	if _, err := fleetCaller(ctx, fleetReadAuthorities...); err != nil {
		return nil, err
	}
	c := args.Criteria
	results, err := r.GetApi(ctx).FleetOperationLines(ctx, args.Token, model.FleetLineSearchCriteria{Pagination: c.Pagination, Posting: c.Posting, Settlement: c.Settlement, Code: c.Code})
	if err != nil {
		return nil, err
	}
	return &FleetLineSearchResultsResolver{M: *results, S: r, C: ctx}, nil
}

func (r *SchemaResolver) PlanBulkChange(ctx context.Context, args struct {
	Request FleetOperationCreateRequestInput
}) (*FleetOperationPlanResolver, error) {
	if _, err := fleetCaller(ctx, fleetWriteAuthorities...); err != nil {
		return nil, err
	}
	plan, err := r.GetApi(ctx).PlanFleetOperation(ctx, args.Request.toModel())
	if err != nil {
		return nil, err
	}
	return &FleetOperationPlanResolver{M: plan}, nil
}

func (r *SchemaResolver) CreateBulkChange(ctx context.Context, args struct {
	Request FleetOperationCreateRequestInput
}) (*FleetOperationResolver, error) {
	initiator, err := fleetCaller(ctx, fleetWriteAuthorities...)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).CreateFleetOperation(ctx, args.Request.toModel(), initiator)
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

type FleetOperationSafetyInputArg struct {
	MaxPostsPerSecond       *int32
	FailureThreshold        *int32
	AmbiguityThreshold      *int32
	ClearFailureThreshold   *bool
	ClearAmbiguityThreshold *bool
}

func (in *FleetOperationSafetyInputArg) toModel() *model.FleetResumeSafety {
	if in == nil {
		return nil
	}
	out := &model.FleetResumeSafety{MaxPostsPerSecond: in.MaxPostsPerSecond, FailureThreshold: in.FailureThreshold, AmbiguityThreshold: in.AmbiguityThreshold}
	out.ClearFailureThreshold = in.ClearFailureThreshold != nil && *in.ClearFailureThreshold
	out.ClearAmbiguityThreshold = in.ClearAmbiguityThreshold != nil && *in.ClearAmbiguityThreshold
	return out
}

// fleetApprover is who the caller is, for a call that approves or takes over an operation.
func fleetApprover(ctx context.Context, name string) model.FleetApprover {
	by := model.FleetApprover{Name: name, Authorities: "device:write fleet:operate"}
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		by.Session = string(claims.SessionID)
	}
	return by
}

func (r *SchemaResolver) PreviewBulkChangeResume(ctx context.Context, args struct {
	Token  string
	Safety *FleetOperationSafetyInputArg
}) (*FleetResumePreviewResolver, error) {
	if _, err := fleetCaller(ctx, fleetWriteAuthorities...); err != nil {
		return nil, err
	}
	p, err := r.GetApi(ctx).PreviewFleetOperationResume(ctx, args.Token, args.Safety.toModel())
	if err != nil {
		return nil, err
	}
	return &FleetResumePreviewResolver{M: *p}, nil
}

func (r *SchemaResolver) StartBulkChange(ctx context.Context, args struct{ Token, ContractDigest string }) (*FleetOperationResolver, error) {
	name, err := fleetCaller(ctx, fleetWriteAuthorities...)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).StartFleetOperation(ctx, args.Token, args.ContractDigest, fleetApprover(ctx, name))
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

// Pause and abort need device:write only: an administrator who has lost fleet:operate can still stop an
// operation, and stopping never widens anything.
func (r *SchemaResolver) PauseBulkChange(ctx context.Context, args struct{ Token string }) (*FleetOperationResolver, error) {
	name, err := fleetCaller(ctx, auth.DeviceWrite)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).PauseFleetOperation(ctx, args.Token, name)
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

func (r *SchemaResolver) AbortBulkChange(ctx context.Context, args struct{ Token string }) (*FleetOperationResolver, error) {
	name, err := fleetCaller(ctx, auth.DeviceWrite)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).AbortFleetOperation(ctx, args.Token, name)
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

func (r *SchemaResolver) ResumeBulkChange(ctx context.Context, args struct {
	Token          string
	ContractDigest string
	Safety         *FleetOperationSafetyInputArg
}) (*FleetOperationResolver, error) {
	name, err := fleetCaller(ctx, fleetWriteAuthorities...)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).ResumeFleetOperation(ctx, args.Token, args.ContractDigest, args.Safety.toModel(), fleetApprover(ctx, name))
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

type FleetResumePreviewResolver struct{ M model.FleetResumePreview }

type FleetAmbiguityResolutionInput struct {
	Token        string
	Ordinals     *[]int32
	AllAmbiguous *bool
	Verdict      string
	Note         string
}

func (r *SchemaResolver) ResolveAmbiguousBulkChangeLines(ctx context.Context, args struct {
	Request FleetAmbiguityResolutionInput
}) (*FleetOperationResolver, error) {
	if _, err := fleetCaller(ctx, fleetWriteAuthorities...); err != nil {
		return nil, err
	}
	in := args.Request
	req := model.FleetAmbiguityResolution{Token: in.Token, Verdict: in.Verdict, Note: in.Note,
		AllAmbiguous: in.AllAmbiguous != nil && *in.AllAmbiguous}
	if in.Ordinals != nil {
		req.Ordinals = *in.Ordinals
	}
	op, err := r.GetApi(ctx).ResolveAmbiguousFleetLines(ctx, req)
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

type FleetReversalRequestInput struct {
	Original         string
	Token            string
	Name             string
	Ordinals         *[]int32
	OverrideChanged  *bool
	OverrideOrdinals *[]int32
	Safety           *FleetOperationCreateSafetyInput
}

func (in FleetReversalRequestInput) toModel() *model.FleetReversalRequest {
	req := &model.FleetReversalRequest{Original: in.Original, Token: in.Token, Name: in.Name, Ordinals: in.Ordinals,
		OverrideChanged: in.OverrideChanged != nil && *in.OverrideChanged}
	if in.OverrideOrdinals != nil {
		req.OverrideOrdinals = *in.OverrideOrdinals
	}
	if in.Safety != nil {
		req.Safety = model.FleetSafetyRequest{MaxPostsPerSecond: in.Safety.MaxPostsPerSecond, FailureThreshold: in.Safety.FailureThreshold,
			AmbiguityThreshold: in.Safety.AmbiguityThreshold, SettleDeadlineSeconds: in.Safety.SettleDeadlineSeconds}
	}
	return req
}

func (r *SchemaResolver) PreviewBulkChangeReversal(ctx context.Context, args struct {
	Request FleetReversalRequestInput
}) (*FleetReversalPreviewResolver, error) {
	if _, err := fleetCaller(ctx, fleetWriteAuthorities...); err != nil {
		return nil, err
	}
	p, err := r.GetApi(ctx).PreviewFleetReversal(ctx, args.Request.toModel())
	if err != nil {
		return nil, err
	}
	return &FleetReversalPreviewResolver{M: *p}, nil
}

func (r *SchemaResolver) ReverseBulkChange(ctx context.Context, args struct {
	Request FleetReversalRequestInput
}) (*FleetOperationResolver, error) {
	initiator, err := fleetCaller(ctx, fleetWriteAuthorities...)
	if err != nil {
		return nil, err
	}
	op, err := r.GetApi(ctx).ReverseFleetOperation(ctx, args.Request.toModel(), initiator)
	if err != nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r, C: ctx}, nil
}

type FleetReversalPreviewResolver struct{ M model.FleetReversalPreview }

func (r *FleetReversalPreviewResolver) ReversibleCount() int32 {
	return clampInt32(int64(r.M.ReversibleCount))
}
func (r *FleetReversalPreviewResolver) ChangedSinceCount() int32 {
	return clampInt32(int64(r.M.ChangedSinceCount))
}
func (r *FleetReversalPreviewResolver) RedactedCount() int32 {
	return clampInt32(int64(r.M.RedactedCount))
}
func (r *FleetReversalPreviewResolver) AlreadyReversedCount() int32 {
	return clampInt32(int64(r.M.AlreadyReversedCount))
}
func (r *FleetReversalPreviewResolver) NotPostedCount() int32 {
	return clampInt32(int64(r.M.NotPostedCount))
}
func (r *FleetReversalPreviewResolver) RequestDigest() string  { return r.M.RequestDigest }
func (r *FleetReversalPreviewResolver) PlanDigest() string     { return r.M.PlanDigest }
func (r *FleetReversalPreviewResolver) ContractDigest() string { return r.M.ContractDigest }

func (r *FleetResumePreviewResolver) WithinContract() bool         { return r.M.WithinContract }
func (r *FleetResumePreviewResolver) ContractVersion() int32       { return r.M.ContractVersion }
func (r *FleetResumePreviewResolver) ContractDigest() string       { return r.M.ContractDigest }
func (r *FleetResumePreviewResolver) Contract() string             { return r.M.Contract }
func (r *FleetResumePreviewResolver) StepUpRequired() bool         { return r.M.StepUpRequired }
func (r *FleetResumePreviewResolver) ThresholdStillExceeded() bool { return r.M.ThresholdStillExceeded }

// ---- resolvers ----

type FleetOperationResolver struct {
	M model.FleetOperation
	S *SchemaResolver
	C context.Context
}

func (r *FleetOperationResolver) Id() gql.ID         { return gql.ID(fmt.Sprint(r.M.ID)) }
func (r *FleetOperationResolver) CreatedAt() *string { return util.FormatTime(r.M.CreatedAt) }
func (r *FleetOperationResolver) UpdatedAt() *string { return util.FormatTime(r.M.UpdatedAt) }
func (r *FleetOperationResolver) DeletedAt() *string { return nil }
func (r *FleetOperationResolver) Token() string      { return r.M.Token }
func (r *FleetOperationResolver) Name() string       { return r.M.Name }
func (r *FleetOperationResolver) Kind() string       { return r.M.Kind }
func (r *FleetOperationResolver) Dispatch() string   { return r.M.Dispatch }
func (r *FleetOperationResolver) Evidence() string   { return r.M.Evidence }
func (r *FleetOperationResolver) PauseReason() *string {
	if r.M.PauseReason == "" {
		return nil
	}
	return &r.M.PauseReason
}

func (r *FleetOperationResolver) runtime() (*model.FleetOperationRuntime, error) {
	return r.S.GetApi(r.C).FleetOperationRuntimeOf(r.C, &r.M)
}

func (r *FleetOperationResolver) HoldCode() (*string, error) {
	rt, err := r.runtime()
	if err != nil || rt == nil || rt.HoldCode == "" {
		return nil, err
	}
	return &rt.HoldCode, nil
}

func (r *FleetOperationResolver) HoldSince() (*string, error) {
	rt, err := r.runtime()
	if err != nil || rt == nil || !rt.HoldSince.Valid {
		return nil, err
	}
	return util.FormatTime(rt.HoldSince.Time), nil
}

// Executor is UNKNOWN for an operation that is RUNNING until the engine reports on itself, and IDLE
// for every state in which nothing is meant to be happening: an unknown is never reported as healthy.
func (r *FleetOperationResolver) Executor() string {
	if r.M.Dispatch == model.FleetDispatchPlanning || r.M.Dispatch == model.FleetDispatchReady {
		return "IDLE"
	}
	return "UNKNOWN"
}

func (r *FleetOperationResolver) LastProgressAt() (*string, error) {
	rt, err := r.runtime()
	if err != nil || rt == nil || !rt.LastProgressAt.Valid {
		return nil, err
	}
	return util.FormatTime(rt.LastProgressAt.Time), nil
}

func (r *FleetOperationResolver) LastErrorCode() (*string, error) {
	rt, err := r.runtime()
	if err != nil || rt == nil || rt.LastErrorCode == "" {
		return nil, err
	}
	return &rt.LastErrorCode, nil
}

func (r *FleetOperationResolver) ExecutionEpoch() int32  { return clampInt32(r.M.ExecutionEpoch) }
func (r *FleetOperationResolver) RequestDigest() string  { return r.M.RequestDigest }
func (r *FleetOperationResolver) PlanDigest() string     { return r.M.PlanDigest }
func (r *FleetOperationResolver) ContractVersion() int32 { return r.M.ContractVersion }
func (r *FleetOperationResolver) ContractDigest() string { return r.M.ContractDigest }
func (r *FleetOperationResolver) Intent() string         { return r.M.Intent }
func (r *FleetOperationResolver) TargetKind() string     { return r.M.TargetKind }
func (r *FleetOperationResolver) TargetCount() int32     { return r.M.TargetCount }
func (r *FleetOperationResolver) Initiator() string      { return r.M.Initiator }
func (r *FleetOperationResolver) StartedAt() *string {
	return fleetTime(r.M.StartedAt.Valid, r.M.StartedAt.Time)
}
func (r *FleetOperationResolver) DispatchedAt() *string {
	return fleetTime(r.M.DispatchedAt.Valid, r.M.DispatchedAt.Time)
}
func (r *FleetOperationResolver) ClosedAt() *string {
	return fleetTime(r.M.ClosedAt.Valid, r.M.ClosedAt.Time)
}
func (r *FleetOperationResolver) ExpiresAt() *string {
	return fleetTime(r.M.ExpiresAt.Valid, r.M.ExpiresAt.Time)
}
func (r *FleetOperationResolver) Grantee() *string {
	if r.M.Grantee == "" {
		return nil
	}
	return &r.M.Grantee
}

func (r *FleetOperationResolver) ReversalOf() (*FleetOperationResolver, error) {
	if !r.M.ReversalOf.Valid {
		return nil, nil
	}
	op, err := r.S.GetApi(r.C).FleetOperationByID(r.C, r.M.ReversalOf.Int64)
	if err != nil || op == nil {
		return nil, err
	}
	return &FleetOperationResolver{M: *op, S: r.S, C: r.C}, nil
}

func (r *FleetOperationResolver) Reversals() ([]*FleetOperationResolver, error) {
	ops, err := r.S.GetApi(r.C).FleetReversalsOf(r.C, &r.M)
	if err != nil {
		return nil, err
	}
	out := make([]*FleetOperationResolver, 0, len(ops))
	for _, op := range ops {
		out = append(out, &FleetOperationResolver{M: op, S: r.S, C: r.C})
	}
	return out, nil
}

func (r *FleetOperationResolver) Contract() (string, error) {
	return r.S.GetApi(r.C).FleetOperationContractBody(r.C, &r.M)
}

func (r *FleetOperationResolver) Safety() *FleetOperationSafetyResolver {
	return &FleetOperationSafetyResolver{M: r.M}
}

func (r *FleetOperationResolver) EvidenceCapability() (*FleetEvidenceCapabilityResolver, error) {
	complete, err := r.S.GetApi(r.C).FleetReportCaptureComplete(r.C, &r.M)
	if err != nil {
		return nil, err
	}
	return &FleetEvidenceCapabilityResolver{brokerFailClosed: r.M.BrokerFailClosed, reportLog: complete}, nil
}

func (r *FleetOperationResolver) Progress() (*FleetOperationProgressResolver, error) {
	p, err := r.S.GetApi(r.C).FleetOperationProgressOf(r.C, &r.M)
	if err != nil {
		return nil, err
	}
	return &FleetOperationProgressResolver{M: *p}, nil
}

func fleetTime(valid bool, t time.Time) *string {
	if !valid {
		return nil
	}
	return util.FormatTime(t)
}

type FleetOperationSafetyResolver struct{ M model.FleetOperation }

func (r *FleetOperationSafetyResolver) MaxPostsPerSecond() int32       { return r.M.MaxPostsPerSecond }
func (r *FleetOperationSafetyResolver) EffectivePostsPerSecond() int32 { return r.M.EffPostsPerSecond }
func (r *FleetOperationSafetyResolver) FailureThreshold() *int32 {
	return nullable32(r.M.FailureThreshold.Valid, r.M.FailureThreshold.Int32)
}
func (r *FleetOperationSafetyResolver) EffectiveFailureThreshold() *int32 {
	return nullable32(r.M.EffFailureThreshold.Valid, r.M.EffFailureThreshold.Int32)
}
func (r *FleetOperationSafetyResolver) AmbiguityThreshold() *int32 {
	return nullable32(r.M.AmbiguityThreshold.Valid, r.M.AmbiguityThreshold.Int32)
}
func (r *FleetOperationSafetyResolver) EffectiveAmbiguityThreshold() *int32 {
	return nullable32(r.M.EffAmbiguityThreshold.Valid, r.M.EffAmbiguityThreshold.Int32)
}
func (r *FleetOperationSafetyResolver) SettleDeadlineSeconds() int32 { return r.M.SettleDeadlineS }

func nullable32(valid bool, v int32) *int32 {
	if !valid {
		return nil
	}
	return &v
}

type FleetEvidenceCapabilityResolver struct{ brokerFailClosed, reportLog bool }

func (r *FleetEvidenceCapabilityResolver) BrokerFailClosed() bool { return r.brokerFailClosed }
func (r *FleetEvidenceCapabilityResolver) ReportLog() bool        { return r.reportLog }
func (r *FleetEvidenceCapabilityResolver) Note() string {
	if !r.reportLog {
		return "Report capture is incomplete for one or more targeted devices. Evidence may rely on latest reported state; intermediate reports may be missing."
	}
	return "A verdict is derived from the first report the device sent for the revision the platform wrote, " +
		"or from its latest reported state where the history is incomplete. A device that never reports is ambiguous, " +
		"not failed. The platform does not claim that every report a device sent was received."
}

type FleetOperationProgressResolver struct{ M model.FleetOperationProgress }

func (r *FleetOperationProgressResolver) Planned() int32          { return r.M.Planned }
func (r *FleetOperationProgressResolver) Posted() int32           { return r.M.Posted }
func (r *FleetOperationProgressResolver) Refused() int32          { return r.M.Refused }
func (r *FleetOperationProgressResolver) Skipped() int32          { return r.M.Skipped }
func (r *FleetOperationProgressResolver) Cancelled() int32        { return r.M.Cancelled }
func (r *FleetOperationProgressResolver) Confirmed() int32        { return r.M.Confirmed }
func (r *FleetOperationProgressResolver) Failed() int32           { return r.M.Failed }
func (r *FleetOperationProgressResolver) Superseded() int32       { return r.M.Superseded }
func (r *FleetOperationProgressResolver) Ambiguous() int32        { return r.M.Ambiguous }
func (r *FleetOperationProgressResolver) Resolved() int32         { return r.M.Resolved }
func (r *FleetOperationProgressResolver) AwaitingEvidence() int32 { return r.M.AwaitingEvidence }

type FleetOperationSearchResultsResolver struct {
	M model.FleetOperationSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *FleetOperationSearchResultsResolver) Results() []*FleetOperationResolver {
	out := make([]*FleetOperationResolver, 0, len(r.M.Results))
	for _, m := range r.M.Results {
		out = append(out, &FleetOperationResolver{M: m, S: r.S, C: r.C})
	}
	return out
}

func (r *FleetOperationSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

type FleetLineSearchResultsResolver struct {
	M model.FleetLineSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *FleetLineSearchResultsResolver) Results() []*FleetOperationLineResolver {
	out := make([]*FleetOperationLineResolver, 0, len(r.M.Results))
	for _, m := range r.M.Results {
		out = append(out, &FleetOperationLineResolver{M: m, S: r.S, C: r.C})
	}
	return out
}

func (r *FleetLineSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

type FleetOperationLineResolver struct {
	M model.FleetOperationLine
	S *SchemaResolver
	C context.Context
}

func (r *FleetOperationLineResolver) ReversedBy() (*FleetOperationLineResolver, error) {
	contra, err := r.S.GetApi(r.C).FleetLineReversedBy(r.C, &r.M)
	if err != nil || contra == nil {
		return nil, err
	}
	return &FleetOperationLineResolver{M: *contra, S: r.S, C: r.C}, nil
}

func (r *FleetOperationLineResolver) ResolutionNote() (*string, error) {
	return r.S.GetApi(r.C).FleetResolutionNote(r.C, &r.M)
}

func fleetStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *FleetOperationLineResolver) Ordinal() int32 { return r.M.Ordinal }
func (r *FleetOperationLineResolver) TargetToken() *string {
	return fleetStr(r.M.TargetKey)
}
func (r *FleetOperationLineResolver) Redacted() bool  { return r.M.RedactedAt.Valid }
func (r *FleetOperationLineResolver) Posting() string { return r.M.Posting }
func (r *FleetOperationLineResolver) Settlement() *string {
	return util.NullStr(r.M.Settlement)
}
func (r *FleetOperationLineResolver) Code() *string   { return fleetStr(r.M.Code) }
func (r *FleetOperationLineResolver) Detail() *string { return fleetStr(r.M.Detail) }
func (r *FleetOperationLineResolver) Prior() *string  { return util.NullStr(r.M.Prior) }
func (r *FleetOperationLineResolver) PostedValue() *string {
	return util.NullStr(r.M.PostedValue)
}
func (r *FleetOperationLineResolver) PostedRevision() *int32 {
	return nullable32(r.M.PostedRevision.Valid, clampInt32(r.M.PostedRevision.Int64))
}
func (r *FleetOperationLineResolver) PostedAt() *string {
	return fleetTime(r.M.PostedAt.Valid, r.M.PostedAt.Time)
}
func (r *FleetOperationLineResolver) SettleBy() *string {
	return fleetTime(r.M.SettleBy.Valid, r.M.SettleBy.Time)
}
func (r *FleetOperationLineResolver) SettledAt() *string {
	return fleetTime(r.M.SettledAt.Valid, r.M.SettledAt.Time)
}
func (r *FleetOperationLineResolver) EvidenceBasis() *string {
	return util.NullStr(r.M.EvidenceBasis)
}
func (r *FleetOperationLineResolver) EvidenceRevision() *int32 {
	return nullable32(r.M.EvidenceRevision.Valid, clampInt32(r.M.EvidenceRevision.Int64))
}
func (r *FleetOperationLineResolver) EvidenceFlag() *string { return fleetStr(r.M.EvidenceFlag) }
func (r *FleetOperationLineResolver) Attempts() int32       { return r.M.Attempts }
func (r *FleetOperationLineResolver) LastErrorCode() *string {
	return fleetStr(r.M.LastErrorCode)
}
func (r *FleetOperationLineResolver) OverrideChanged() bool { return r.M.OverrideChanged }
func (r *FleetOperationLineResolver) ResolvedBy() *string   { return util.NullStr(r.M.ResolvedBy) }
func (r *FleetOperationLineResolver) ResolvedAt() *string {
	return fleetTime(r.M.ResolvedAt.Valid, r.M.ResolvedAt.Time)
}

type FleetOperationPlanResolver struct{ M *model.FleetPlan }

func (r *FleetOperationPlanResolver) AdmittedCount() int32   { return clampInt32(int64(r.M.Admitted)) }
func (r *FleetOperationPlanResolver) RefusedCount() int32    { return clampInt32(int64(r.M.Refused)) }
func (r *FleetOperationPlanResolver) RequestDigest() string  { return r.M.RequestDigest }
func (r *FleetOperationPlanResolver) PlanDigest() string     { return r.M.PlanDigest }
func (r *FleetOperationPlanResolver) ContractDigest() string { return r.M.ContractDigest }

func (r *FleetOperationPlanResolver) Refusals() []*FleetPlanRefusalResolver {
	out := make([]*FleetPlanRefusalResolver, 0, len(r.M.Refusals))
	for _, m := range r.M.Refusals {
		out = append(out, &FleetPlanRefusalResolver{M: m})
	}
	return out
}

func (r *FleetOperationPlanResolver) RefusalCounts() []*FleetPlanRefusalCountResolver {
	out := make([]*FleetPlanRefusalCountResolver, 0, len(r.M.RefusalCounts))
	for _, code := range []string{model.FleetLineTargetNotFound, model.FleetLineDuplicateTarget} {
		if n, ok := r.M.RefusalCounts[code]; ok {
			out = append(out, &FleetPlanRefusalCountResolver{code: code, n: n})
		}
	}
	return out
}

type FleetPlanRefusalResolver struct{ M model.FleetPlanRefusal }

func (r *FleetPlanRefusalResolver) Ordinal() int32 { return r.M.Ordinal }
func (r *FleetPlanRefusalResolver) Token() string  { return r.M.Token }
func (r *FleetPlanRefusalResolver) Code() string   { return r.M.Code }

type FleetPlanRefusalCountResolver struct {
	code string
	n    int
}

func (r *FleetPlanRefusalCountResolver) Code() string { return r.code }
func (r *FleetPlanRefusalCountResolver) Count() int32 { return clampInt32(int64(r.n)) }
