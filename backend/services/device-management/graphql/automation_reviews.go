// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// Read authorization must precede any storage access. Authorize itself refuses
// scoped grants, so translate that case before it to retain the actiongate code.
func authorizeReviewRead(ctx context.Context) error {
	if claims, ok := auth.ClaimsFromContext(ctx); ok && len(claims.ScopeAnchors) != 0 {
		return actiongate.Refusal(actiongate.CodeScopedNotSupported, "automation review reads do not support scoped grants", true)
	}
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return err
	}
	return auth.Authorize(ctx, auth.EventRead)
}

func (r *SchemaResolver) AutomationReview(ctx context.Context, args struct{ Token string }) (*AutomationReviewResolver, error) {
	if err := authorizeReviewRead(ctx); err != nil {
		return nil, err
	}
	row, err := r.GetApi(ctx).AutomationReviewByToken(ctx, args.Token)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewResolver{M: *row, S: r}, nil
}

type AutomationReviewSearchCriteria struct {
	ProfileToken         string
	State                *string
	MatchingCurrentDraft *bool
	PageNumber           int32
	PageSize             int32
}

type AutomationReviewSearchResultsResolver struct {
	M model.AutomationReviewSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AutomationReviewSearchResultsResolver) Results() []*AutomationReviewResolver {
	out := make([]*AutomationReviewResolver, 0, len(r.M.Results))
	for _, row := range r.M.Results {
		out = append(out, &AutomationReviewResolver{M: row, S: r.S})
	}
	return out
}

func (r *AutomationReviewSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}

func (r *SchemaResolver) AutomationReviews(ctx context.Context, args struct {
	Criteria AutomationReviewSearchCriteria
}) (*AutomationReviewSearchResultsResolver, error) {
	if err := authorizeReviewRead(ctx); err != nil {
		return nil, err
	}
	in := args.Criteria
	out, err := r.GetApi(ctx).AutomationReviews(ctx, model.AutomationReviewSearchCriteria{
		Pagination:   rdb.Pagination{PageNumber: in.PageNumber, PageSize: in.PageSize},
		ProfileToken: in.ProfileToken, State: in.State, MatchingCurrentDraft: in.MatchingCurrentDraft,
	})
	if err != nil {
		return nil, err
	}
	return &AutomationReviewSearchResultsResolver{M: *out, S: r, C: ctx}, nil
}

type AutomationReviewWindowInput struct{ Role, Start, End string }
type AutomationReviewExpectationInputValue struct {
	Window string
	Name   *string
	Expect string
}
type RunAutomationReviewInput struct {
	ProfileToken       string
	RuleToken          *string
	DeviceTokens       []string
	Windows            []AutomationReviewWindowInput
	Expectations       *[]AutomationReviewExpectationInputValue
	ShiftWindowSeconds *int32
	RequestId          string
}

func (r *SchemaResolver) RunAutomationReview(ctx context.Context, args struct{ Input RunAutomationReviewInput }) (*AutomationReviewResolver, error) {
	person, err := model.AuthorizeReviewPerson(ctx, auth.DeviceWrite)
	if err != nil {
		return nil, err
	}
	in := args.Input
	request := model.RunAutomationReviewInput{ProfileToken: in.ProfileToken, RuleToken: in.RuleToken,
		DeviceTokens: in.DeviceTokens, ShiftWindowSeconds: in.ShiftWindowSeconds, RequestId: in.RequestId}
	for _, w := range in.Windows {
		start, err := time.Parse(time.RFC3339Nano, w.Start)
		if err != nil {
			return nil, &model.InvalidArgumentError{Msg: "window start must be an RFC3339 timestamp"}
		}
		end, err := time.Parse(time.RFC3339Nano, w.End)
		if err != nil {
			return nil, &model.InvalidArgumentError{Msg: "window end must be an RFC3339 timestamp"}
		}
		request.Windows = append(request.Windows, model.ReviewWindow{Role: w.Role, Start: start, End: end})
	}
	if in.Expectations != nil {
		for _, e := range *in.Expectations {
			name := ""
			if e.Name != nil {
				name = *e.Name
			}
			request.Expectations = append(request.Expectations, model.ReviewExpectation{Window: e.Window, Name: name, Expect: e.Expect})
		}
	}
	row, err := r.GetApi(ctx).RunAutomationReview(ctx, person, request)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewResolver{M: *row, S: r}, nil
}

func (r *SchemaResolver) ApproveAutomationReview(ctx context.Context, args struct{ Token, Digest string }) (*AutomationReviewResolver, error) {
	person, err := model.AuthorizeReviewPerson(ctx, auth.DeviceWrite)
	if err != nil {
		return nil, err
	}
	row, err := r.GetApi(ctx).ApproveAutomationReview(ctx, person, args.Token, args.Digest)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewResolver{M: *row, S: r}, nil
}

func (r *SchemaResolver) RejectAutomationReview(ctx context.Context, args struct {
	Token string
	Note  *string
}) (*AutomationReviewResolver, error) {
	person, err := model.AuthorizeReviewPerson(ctx, auth.DeviceWrite)
	if err != nil {
		return nil, err
	}
	row, err := r.GetApi(ctx).RejectAutomationReview(ctx, person, args.Token, args.Note)
	if err != nil {
		return nil, err
	}
	return &AutomationReviewResolver{M: *row, S: r}, nil
}

// These views mirror event-processing/internal/review JSON without crossing module
// boundaries. Durations are integer nanoseconds; sequence bounds are uint64 values.
type reviewApproval struct {
	Principal      string    `json:"sub"`
	ApprovedAt     time.Time `json:"at"`
	PolicyRevision int32     `json:"policyRevision"`
}

type AutomationReviewApprovalResolver struct{ v *reviewApproval }

func (r *AutomationReviewApprovalResolver) Principal() string     { return r.v.Principal }
func (r *AutomationReviewApprovalResolver) ApprovedAt() string    { return rfc3339(r.v.ApprovedAt) }
func (r *AutomationReviewApprovalResolver) PolicyRevision() int32 { return r.v.PolicyRevision }

type reviewProfileChange struct {
	Area       string `json:"area"`
	Key        string `json:"key"`
	Change     string `json:"change"`
	Automation bool   `json:"automation"`
}

type AutomationReviewProfileChangeResolver struct{ v *reviewProfileChange }

func (r *AutomationReviewProfileChangeResolver) Area() string     { return r.v.Area }
func (r *AutomationReviewProfileChangeResolver) Key() string      { return r.v.Key }
func (r *AutomationReviewProfileChangeResolver) Change() string   { return r.v.Change }
func (r *AutomationReviewProfileChangeResolver) Automation() bool { return r.v.Automation }

type reviewWindowSpec struct {
	Role  string    `json:"role"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type AutomationReviewWindowSpecResolver struct{ v *reviewWindowSpec }

func (r *AutomationReviewWindowSpecResolver) Role() string  { return r.v.Role }
func (r *AutomationReviewWindowSpecResolver) Start() string { return rfc3339(r.v.Start) }
func (r *AutomationReviewWindowSpecResolver) End() string   { return rfc3339(r.v.End) }

type reviewExpectationInput struct {
	Window string  `json:"window"`
	Name   *string `json:"name"`
	Expect string  `json:"expect"`
}

type AutomationReviewExpectationInputResolver struct{ v *reviewExpectationInput }

func (r *AutomationReviewExpectationInputResolver) Window() string { return r.v.Window }
func (r *AutomationReviewExpectationInputResolver) Name() *string  { return r.v.Name }
func (r *AutomationReviewExpectationInputResolver) Expect() string { return r.v.Expect }

type reviewFidelity struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
	Label   string   `json:"label"`
}

type AutomationReviewFidelityResolver struct{ v *reviewFidelity }

func (r *AutomationReviewFidelityResolver) Status() string    { return r.v.Status }
func (r *AutomationReviewFidelityResolver) Reasons() []string { return r.v.Reasons }
func (r *AutomationReviewFidelityResolver) Label() string     { return r.v.Label }

type reviewExpectationResult struct {
	Index          int32            `json:"index"`
	Name           string           `json:"name"`
	Window         string           `json:"window"`
	Verdict        string           `json:"verdict"`
	Baseline       string           `json:"baseline"`
	Reason         string           `json:"reason"`
	BaselineReason string           `json:"baselineReason"`
	Evidence       []*reviewReading `json:"evidence"`
}

type AutomationReviewExpectationResultResolver struct{ v *reviewExpectationResult }

func (r *AutomationReviewExpectationResultResolver) Index() int32     { return r.v.Index }
func (r *AutomationReviewExpectationResultResolver) Name() string     { return r.v.Name }
func (r *AutomationReviewExpectationResultResolver) Window() string   { return r.v.Window }
func (r *AutomationReviewExpectationResultResolver) Verdict() string  { return r.v.Verdict }
func (r *AutomationReviewExpectationResultResolver) Baseline() string { return r.v.Baseline }
func (r *AutomationReviewExpectationResultResolver) Reason() string   { return r.v.Reason }
func (r *AutomationReviewExpectationResultResolver) BaselineReason() string {
	return r.v.BaselineReason
}
func (r *AutomationReviewExpectationResultResolver) Evidence() []*AutomationReviewReadingResolver {
	out := make([]*AutomationReviewReadingResolver, 0, len(r.v.Evidence))
	for _, v := range r.v.Evidence {
		out = append(out, &AutomationReviewReadingResolver{v: v})
	}
	return out
}

type reviewReading struct {
	Device      string        `json:"device"`
	At          time.Time     `json:"at"`
	Value       float64       `json:"value"`
	SpanSeconds time.Duration `json:"span"`
	HasValue    bool          `json:"hasValue"`
}

type AutomationReviewReadingResolver struct{ v *reviewReading }

func (r *AutomationReviewReadingResolver) Device() string       { return r.v.Device }
func (r *AutomationReviewReadingResolver) At() string           { return rfc3339(r.v.At) }
func (r *AutomationReviewReadingResolver) Value() *float64      { return reviewReadingValue(r.v) }
func (r *AutomationReviewReadingResolver) SpanSeconds() float64 { return r.v.SpanSeconds.Seconds() }

type reviewWindowResult struct {
	Role             string                  `json:"role"`
	Start            time.Time               `json:"start"`
	End              time.Time               `json:"end"`
	LeadInStart      time.Time               `json:"leadInStart"`
	InputDigest      string                  `json:"inputDigest"`
	Degraded         []string                `json:"degraded"`
	Counts           *reviewChangeCounts     `json:"counts"`
	ChangesTruncated bool                    `json:"changesTruncated"`
	Devices          []*reviewDeviceCoverage `json:"devices"`
	Changes          []*reviewChange         `json:"changes"`
	Bounds           struct {
		Stream   string `json:"stream"`
		FirstSeq uint64 `json:"firstSeq"`
		LastSeq  uint64 `json:"lastSeq"`
	} `json:"bounds"`
}

type AutomationReviewWindowResultResolver struct{ v *reviewWindowResult }

func (r *AutomationReviewWindowResultResolver) Role() string        { return r.v.Role }
func (r *AutomationReviewWindowResultResolver) Start() string       { return rfc3339(r.v.Start) }
func (r *AutomationReviewWindowResultResolver) End() string         { return rfc3339(r.v.End) }
func (r *AutomationReviewWindowResultResolver) LeadInStart() string { return rfc3339(r.v.LeadInStart) }
func (r *AutomationReviewWindowResultResolver) Stream() string      { return r.v.Bounds.Stream }
func (r *AutomationReviewWindowResultResolver) FirstSeq() string {
	return strconv.FormatUint(r.v.Bounds.FirstSeq, 10)
}
func (r *AutomationReviewWindowResultResolver) LastSeq() string {
	return strconv.FormatUint(r.v.Bounds.LastSeq, 10)
}
func (r *AutomationReviewWindowResultResolver) InputDigest() string { return r.v.InputDigest }
func (r *AutomationReviewWindowResultResolver) Degraded() []string  { return r.v.Degraded }
func (r *AutomationReviewWindowResultResolver) Counts() *AutomationReviewChangeCountsResolver {
	if r.v.Counts == nil {
		return nil
	}
	return &AutomationReviewChangeCountsResolver{v: r.v.Counts}
}
func (r *AutomationReviewWindowResultResolver) ChangesTruncated() bool { return r.v.ChangesTruncated }
func (r *AutomationReviewWindowResultResolver) Devices() []*AutomationReviewDeviceCoverageResolver {
	out := make([]*AutomationReviewDeviceCoverageResolver, 0, len(r.v.Devices))
	for _, v := range r.v.Devices {
		out = append(out, &AutomationReviewDeviceCoverageResolver{v: v})
	}
	return out
}
func (r *AutomationReviewWindowResultResolver) Changes() []*AutomationReviewChangeResolver {
	out := make([]*AutomationReviewChangeResolver, 0, len(r.v.Changes))
	for _, v := range r.v.Changes {
		out = append(out, &AutomationReviewChangeResolver{v: v})
	}
	return out
}

type reviewChangeCounts struct {
	Unchanged      int32 `json:"unchanged"`
	Added          int32 `json:"added"`
	Removed        int32 `json:"removed"`
	Shifted        int32 `json:"shifted"`
	ContentChanged int32 `json:"contentChanged"`
}

type AutomationReviewChangeCountsResolver struct{ v *reviewChangeCounts }

func (r *AutomationReviewChangeCountsResolver) Unchanged() int32      { return r.v.Unchanged }
func (r *AutomationReviewChangeCountsResolver) Added() int32          { return r.v.Added }
func (r *AutomationReviewChangeCountsResolver) Removed() int32        { return r.v.Removed }
func (r *AutomationReviewChangeCountsResolver) Shifted() int32        { return r.v.Shifted }
func (r *AutomationReviewChangeCountsResolver) ContentChanged() int32 { return r.v.ContentChanged }

type reviewDeviceCoverage struct {
	Device     string           `json:"device"`
	Readings   map[string]int32 `json:"readings"`
	Versions   map[string]int32 `json:"versions"`
	StateBase  string           `json:"stateBase"`
	StateDraft string           `json:"stateDraft"`
	Divergence *time.Time       `json:"divergence"`
	LateBase   int32            `json:"lateBase"`
	LateDraft  int32            `json:"lateDraft"`
	First      time.Time        `json:"first"`
	Last       time.Time        `json:"last"`
}

type AutomationReviewDeviceCoverageResolver struct{ v *reviewDeviceCoverage }

func (r *AutomationReviewDeviceCoverageResolver) Device() string { return r.v.Device }
func (r *AutomationReviewDeviceCoverageResolver) Readings() []*AutomationReviewMetricCountResolver {
	return reviewMetricCounts(r.v.Readings)
}
func (r *AutomationReviewDeviceCoverageResolver) Versions() []*AutomationReviewMetricCountResolver {
	return reviewMetricCounts(r.v.Versions)
}
func (r *AutomationReviewDeviceCoverageResolver) StateBase() string  { return r.v.StateBase }
func (r *AutomationReviewDeviceCoverageResolver) StateDraft() string { return r.v.StateDraft }
func (r *AutomationReviewDeviceCoverageResolver) Divergence() *string {
	return rfc3339Ptr(r.v.Divergence)
}
func (r *AutomationReviewDeviceCoverageResolver) LateBase() int32  { return r.v.LateBase }
func (r *AutomationReviewDeviceCoverageResolver) LateDraft() int32 { return r.v.LateDraft }
func (r *AutomationReviewDeviceCoverageResolver) First() *string {
	return reviewOptionalTime(r.v.First)
}
func (r *AutomationReviewDeviceCoverageResolver) Last() *string { return reviewOptionalTime(r.v.Last) }

type reviewMetricCount struct {
	Key   string `json:"key"`
	Count int32  `json:"count"`
}

type AutomationReviewMetricCountResolver struct{ v *reviewMetricCount }

func (r *AutomationReviewMetricCountResolver) Key() string  { return r.v.Key }
func (r *AutomationReviewMetricCountResolver) Count() int32 { return r.v.Count }

type reviewChange struct {
	Kind            string               `json:"kind"`
	Device          string               `json:"device"`
	Subject         string               `json:"subject"`
	Discriminator   string               `json:"discriminator"`
	BaseAt          *time.Time           `json:"base"`
	DraftAt         *time.Time           `json:"draft"`
	ShiftSeconds    time.Duration        `json:"shift"`
	BaseContent     *reviewIntentContent `json:"baseContent"`
	DraftContent    *reviewIntentContent `json:"draftContent"`
	ContentChanged  bool                 `json:"contentChanged"`
	Reading         *reviewReading       `json:"reading"`
	AfterDivergence bool                 `json:"afterDivergence"`
}

type AutomationReviewChangeResolver struct{ v *reviewChange }

func (r *AutomationReviewChangeResolver) Kind() string          { return r.v.Kind }
func (r *AutomationReviewChangeResolver) Device() string        { return r.v.Device }
func (r *AutomationReviewChangeResolver) Subject() string       { return r.v.Subject }
func (r *AutomationReviewChangeResolver) Discriminator() string { return r.v.Discriminator }
func (r *AutomationReviewChangeResolver) BaseAt() *string       { return rfc3339Ptr(r.v.BaseAt) }
func (r *AutomationReviewChangeResolver) DraftAt() *string      { return rfc3339Ptr(r.v.DraftAt) }
func (r *AutomationReviewChangeResolver) ShiftSeconds() float64 { return r.v.ShiftSeconds.Seconds() }
func (r *AutomationReviewChangeResolver) BaseContent() *AutomationReviewIntentContentResolver {
	if r.v.BaseContent == nil {
		return nil
	}
	return &AutomationReviewIntentContentResolver{v: r.v.BaseContent}
}
func (r *AutomationReviewChangeResolver) DraftContent() *AutomationReviewIntentContentResolver {
	if r.v.DraftContent == nil {
		return nil
	}
	return &AutomationReviewIntentContentResolver{v: r.v.DraftContent}
}
func (r *AutomationReviewChangeResolver) ContentChanged() bool { return r.v.ContentChanged }
func (r *AutomationReviewChangeResolver) Reading() *AutomationReviewReadingResolver {
	if r.v.Reading == nil {
		return nil
	}
	return &AutomationReviewReadingResolver{v: r.v.Reading}
}
func (r *AutomationReviewChangeResolver) AfterDivergence() bool { return r.v.AfterDivergence }

type reviewIntentContent struct {
	Severity              *string        `json:"severity"`
	Payload               *string        `json:"payload"`
	RelativeExpirySeconds *time.Duration `json:"relativeExpiry"`
	ExpiryLabel           *string        `json:"expiryLabel"`
	ConfigDigest          *string        `json:"configDigest"`
	BodyDigest            *string        `json:"bodyDigest"`
	Method                *string        `json:"method"`
}

type AutomationReviewIntentContentResolver struct{ v *reviewIntentContent }

func (r *AutomationReviewIntentContentResolver) Severity() *string { return r.v.Severity }
func (r *AutomationReviewIntentContentResolver) Payload() *string  { return r.v.Payload }
func (r *AutomationReviewIntentContentResolver) RelativeExpirySeconds() *float64 {
	return reviewDuration(r.v.RelativeExpirySeconds)
}
func (r *AutomationReviewIntentContentResolver) ExpiryLabel() *string  { return r.v.ExpiryLabel }
func (r *AutomationReviewIntentContentResolver) ConfigDigest() *string { return r.v.ConfigDigest }
func (r *AutomationReviewIntentContentResolver) BodyDigest() *string   { return r.v.BodyDigest }
func (r *AutomationReviewIntentContentResolver) Method() *string       { return r.v.Method }

func reviewReadingValue(v *reviewReading) *float64 {
	if !v.HasValue {
		return nil
	}
	return &v.Value
}
func reviewOptionalTime(v time.Time) *string {
	if v.IsZero() {
		return nil
	}
	s := rfc3339(v)
	return &s
}
func reviewDuration(v *time.Duration) *float64 {
	if v == nil {
		return nil
	}
	s := v.Seconds()
	return &s
}
func reviewMetricCounts(v map[string]int32) []*AutomationReviewMetricCountResolver {
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*AutomationReviewMetricCountResolver, 0, len(keys))
	for _, key := range keys {
		out = append(out, &AutomationReviewMetricCountResolver{v: &reviewMetricCount{Key: key, Count: v[key]}})
	}
	return out
}

type reviewResult struct {
	Fidelity     *reviewFidelity            `json:"fidelity"`
	Windows      []*reviewWindowResult      `json:"windows"`
	Expectations []*reviewExpectationResult `json:"expectations"`
	Reasons      []string                   `json:"reasons"`
}

// AutomationReviewResolver parses the stored result once, including when sibling
// fields are resolved concurrently. Each parsed field propagates parse errors.
type AutomationReviewResolver struct {
	M        model.AutomationReview
	S        *SchemaResolver
	once     sync.Once
	parsed   reviewResult
	parseErr error
}

func (r *AutomationReviewResolver) parseResult() error {
	r.once.Do(func() { r.parseErr = reviewObject([]byte(r.M.Result), &r.parsed) })
	return r.parseErr
}

func reviewObject(raw []byte, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("stored review value must be an object")
	}
	return json.Unmarshal(trimmed, out)
}

func reviewList[T any](raw []byte) (*[]T, error) {
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("stored review list is missing")
	}
	return &out, nil
}

func (r *AutomationReviewResolver) ProfileToken(ctx context.Context) (*string, error) {
	var p model.DeviceProfile
	if err := r.S.GetApi(ctx).RDB.DB(ctx).First(&p, r.M.DeviceProfileId).Error; err != nil {
		return nil, err
	}
	return &p.Token, nil
}
func (r *AutomationReviewResolver) MatchesCurrentDraft(ctx context.Context) (*bool, error) {
	digest, err := r.S.GetApi(ctx).CurrentDraftDigest(ctx, r.M.DeviceProfileId)
	if err != nil {
		return nil, err
	}
	match := actiongate.DigestEqual(digest, r.M.DraftDigest)
	return &match, nil
}
func (r *AutomationReviewResolver) Token() string            { return r.M.Token }
func (r *AutomationReviewResolver) Kind() string             { return r.M.Kind }
func (r *AutomationReviewResolver) RuleToken() *string       { return r.M.RuleToken }
func (r *AutomationReviewResolver) State() string            { return r.M.State }
func (r *AutomationReviewResolver) Outcome() string          { return r.M.Outcome }
func (r *AutomationReviewResolver) ReviewDigest() string     { return r.M.ReviewDigest }
func (r *AutomationReviewResolver) DraftDigest() string      { return r.M.DraftDigest }
func (r *AutomationReviewResolver) BaselineDigest() string   { return r.M.BaselineDigest }
func (r *AutomationReviewResolver) BaselineVersion() int32   { return r.M.BaselineVersion }
func (r *AutomationReviewResolver) CreatedBy() string        { return r.M.CreatedBy }
func (r *AutomationReviewResolver) CreatedAt() string        { return rfc3339(r.M.CreatedAt) }
func (r *AutomationReviewResolver) RequiredApprovals() int32 { return int32(r.M.RequiredApprovals) }
func (r *AutomationReviewResolver) ApprovedAt() *string      { return rfc3339Ptr(r.M.ApprovedAt) }
func (r *AutomationReviewResolver) ExpiresAt() *string       { return rfc3339Ptr(r.M.ExpiresAt) }
func (r *AutomationReviewResolver) DecidedBy() *string       { return r.M.DecidedBy }
func (r *AutomationReviewResolver) DecidedAt() *string       { return rfc3339Ptr(r.M.DecidedAt) }
func (r *AutomationReviewResolver) Note() *string            { return r.M.DecidedNote }
func (r *AutomationReviewResolver) InputDigest() *string     { return &r.M.InputDigest }
func (r *AutomationReviewResolver) ResultDigest() *string    { return &r.M.ResultDigest }
func (r *AutomationReviewResolver) EngineBuild() *string     { return &r.M.EngineBuild }
func (r *AutomationReviewResolver) Result() *string          { return &r.M.Result }

func (r *AutomationReviewResolver) DraftAuthors() (*[]string, error) {
	return reviewList[string](r.M.DraftAuthors)
}
func (r *AutomationReviewResolver) Approvals() (*[]*AutomationReviewApprovalResolver, error) {
	v, err := reviewList[*reviewApproval](r.M.Approvals)
	if err != nil {
		return nil, err
	}
	out := make([]*AutomationReviewApprovalResolver, 0, len(*v))
	for _, x := range *v {
		if x == nil {
			return nil, fmt.Errorf("stored review list contains null")
		}
		out = append(out, &AutomationReviewApprovalResolver{v: x})
	}
	return &out, nil
}
func (r *AutomationReviewResolver) ProfileChanges() (*[]*AutomationReviewProfileChangeResolver, error) {
	v, err := reviewList[*reviewProfileChange](r.M.ProfileChanges)
	if err != nil {
		return nil, err
	}
	out := make([]*AutomationReviewProfileChangeResolver, 0, len(*v))
	for _, x := range *v {
		if x == nil {
			return nil, fmt.Errorf("stored review list contains null")
		}
		out = append(out, &AutomationReviewProfileChangeResolver{v: x})
	}
	return &out, nil
}
func (r *AutomationReviewResolver) ExpectationInputs() (*[]*AutomationReviewExpectationInputResolver, error) {
	v, err := reviewList[*reviewExpectationInput](r.M.Expectations)
	if err != nil {
		return nil, err
	}
	out := make([]*AutomationReviewExpectationInputResolver, 0, len(*v))
	for _, x := range *v {
		if x == nil {
			return nil, fmt.Errorf("stored review list contains null")
		}
		out = append(out, &AutomationReviewExpectationInputResolver{v: x})
	}
	return &out, nil
}

type reviewScope struct {
	Devices            []string            `json:"devices"`
	Windows            []*reviewWindowSpec `json:"windows"`
	ShiftWindowSeconds *int32              `json:"shiftWindowSeconds"`
}

func (r *AutomationReviewResolver) scope() (*reviewScope, error) {
	var v reviewScope
	if err := reviewObject(r.M.Scope, &v); err != nil {
		return nil, err
	}
	return &v, nil
}
func (r *AutomationReviewResolver) DeviceTokens() (*[]string, error) {
	v, err := r.scope()
	if err != nil {
		return nil, err
	}
	if v.Devices == nil {
		return nil, fmt.Errorf("stored review devices are missing")
	}
	return &v.Devices, nil
}
func (r *AutomationReviewResolver) Windows() (*[]*AutomationReviewWindowSpecResolver, error) {
	v, err := r.scope()
	if err != nil {
		return nil, err
	}
	if v.Windows == nil {
		return nil, fmt.Errorf("stored review windows are missing")
	}
	out := make([]*AutomationReviewWindowSpecResolver, 0, len(v.Windows))
	for _, w := range v.Windows {
		if w == nil {
			return nil, fmt.Errorf("stored review window is null")
		}
		out = append(out, &AutomationReviewWindowSpecResolver{v: w})
	}
	return &out, nil
}
func (r *AutomationReviewResolver) ShiftWindowSeconds() (*int32, error) {
	v, err := r.scope()
	if err != nil {
		return nil, err
	}
	return v.ShiftWindowSeconds, nil
}
func (r *AutomationReviewResolver) Reasons() (*[]string, error) {
	if err := r.parseResult(); err != nil {
		return nil, err
	}
	out := []string{}
	if r.M.Kind == model.ReviewKindUnreviewable {
		if r.parsed.Reasons == nil {
			return nil, fmt.Errorf("stored review reasons are missing")
		}
		out = r.parsed.Reasons
	}
	return &out, nil
}
func (r *AutomationReviewResolver) Fidelity() (*AutomationReviewFidelityResolver, error) {
	if err := r.parseResult(); err != nil {
		return nil, err
	}
	if r.parsed.Fidelity == nil {
		if r.M.Kind == model.ReviewKindRuleChange {
			return nil, fmt.Errorf("stored comparison fidelity is missing")
		}
		return nil, nil
	}
	return &AutomationReviewFidelityResolver{v: r.parsed.Fidelity}, nil
}
func (r *AutomationReviewResolver) Expectations() (*[]*AutomationReviewExpectationResultResolver, error) {
	if err := r.parseResult(); err != nil {
		return nil, err
	}
	if r.parsed.Expectations == nil {
		// EP encodes a nil slice as null when no expectations were supplied.
		return nil, nil
	}
	out := make([]*AutomationReviewExpectationResultResolver, 0, len(r.parsed.Expectations))
	for _, v := range r.parsed.Expectations {
		if v == nil {
			return nil, fmt.Errorf("stored review list contains null")
		}
		out = append(out, &AutomationReviewExpectationResultResolver{v: v})
	}
	return &out, nil
}
func (r *AutomationReviewResolver) WindowResults() (*[]*AutomationReviewWindowResultResolver, error) {
	if err := r.parseResult(); err != nil {
		return nil, err
	}
	if r.parsed.Windows == nil {
		if r.M.Kind == model.ReviewKindRuleChange {
			return nil, fmt.Errorf("stored comparison windows are missing")
		}
		return nil, nil
	}
	out := make([]*AutomationReviewWindowResultResolver, 0, len(r.parsed.Windows))
	for _, v := range r.parsed.Windows {
		if v == nil {
			return nil, fmt.Errorf("stored review list contains null")
		}
		out = append(out, &AutomationReviewWindowResultResolver{v: v})
	}
	return &out, nil
}

func (r *AutomationReviewResolver) ConsumedVersion() *int32 {
	return r.M.ConsumedVersion
}
