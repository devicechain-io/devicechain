// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// The planner: it turns a create request into the frozen list of lines and the three digests,
// and writes nothing. planFleetOperation and the create path run THIS code, so what an operator
// previews is what create stores.

// Fleet-operation kinds known to this build.
const FleetKindConfigSet = "config.set"

// Refusal codes a plan or create can answer. They ride extensions.code.
const (
	FleetCodeConflict          = "CONFLICT"
	FleetCodeKindNotExecutable = "KIND_NOT_EXECUTABLE"
	FleetCodeOperationTooLarge = "OPERATION_TOO_LARGE"
	FleetCodeInputTooLarge     = "INPUT_TOO_LARGE"
	FleetCodeQuotaExceeded     = "QUOTA_EXCEEDED"
	FleetCodeAdmissionBusy     = "ADMISSION_BUSY"
	FleetCodeInvalidRequest    = "INVALID_REQUEST"
	FleetCodeAgentNotPermitted = "AGENT_NOT_PERMITTED"

	// Plan-time line refusals.
	FleetLineTargetNotFound  = "TARGET_NOT_FOUND"
	FleetLineDuplicateTarget = "DUPLICATE_TARGET"
)

// MaxFleetPlanRefusals caps the refusals a plan lists per code; the per-code counts are complete.
const MaxFleetPlanRefusals = 100

// FleetOperationError is a refusal with a stable code on extensions.code.
type FleetOperationError struct {
	Code    string
	Message string
	// Quota names the budget a QUOTA_EXCEEDED refusal hit.
	Quota string
}

func (e *FleetOperationError) Error() string { return e.Message }

func (e *FleetOperationError) Extensions() map[string]any {
	ext := map[string]any{"code": e.Code}
	if e.Quota != "" {
		ext["quota"] = e.Quota
	}
	return ext
}

func fleetErr(code, format string, args ...any) *FleetOperationError {
	return &FleetOperationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// FleetConfigSetIntent is the intent of a config.set operation: one declared SHARED key, set or
// unset. Its canonical JSON is what the digests cover.
type FleetConfigSetIntent struct {
	Key       string  `json:"key"`
	ValueType string  `json:"valueType,omitempty"`
	Value     *string `json:"value,omitempty"`
	Unset     bool    `json:"unset"`
}

// FleetSafetyRequest is the safety contract a creator asks for. A nil rate or deadline takes the
// documented default; a nil threshold means none.
type FleetSafetyRequest struct {
	MaxPostsPerSecond     *int32
	FailureThreshold      *int32
	AmbiguityThreshold    *int32
	SettleDeadlineSeconds *int32
}

// FleetOperationCreateRequest is a plan or create request.
type FleetOperationCreateRequest struct {
	Token        string
	Name         string
	Kind         string
	ConfigSet    *FleetConfigSetIntent
	DeviceTokens []string
	Safety       FleetSafetyRequest
}

// FleetPlanLine is one planned line: admitted when Code is empty, refused at plan time otherwise.
type FleetPlanLine struct {
	Ordinal  int32
	Token    string
	TargetId *uint
	Code     string
}

// FleetPlanRefusal is one refused input, with the token the operator supplied.
type FleetPlanRefusal struct {
	Ordinal int32
	Token   string
	Code    string
}

// FleetPlan is the result of planning. It is also the unit create stores.
type FleetPlan struct {
	Kind           string
	Intent         string
	Safety         ledger.Safety
	Lines          []FleetPlanLine
	Admitted       int
	Refused        int
	RefusalCounts  map[string]int
	Refusals       []FleetPlanRefusal
	RequestDigest  string
	PlanDigest     string
	ContractDigest string
	ContractBody   string
}

// fleetNormalized is a validated request: everything that does not need the database.
type fleetNormalized struct {
	kind          string
	intent        any
	intentJSON    string
	tokens        []string
	safety        ledger.Safety
	requestDigest string
}

// normalizeFleetRequest validates req and computes the request digest, touching no database. The
// create path calls it first so the replay probe can run before any target is resolved.
func normalizeFleetRequest(req *FleetOperationCreateRequest) (*fleetNormalized, error) {
	if req == nil {
		return nil, fleetErr(FleetCodeInvalidRequest, "a request is required")
	}
	if req.Kind != FleetKindConfigSet {
		return nil, fleetErr(FleetCodeKindNotExecutable, "kind %q is not executable by this build", req.Kind)
	}
	if req.ConfigSet == nil {
		return nil, fleetErr(FleetCodeInvalidRequest, "configSet is required for kind %s", FleetKindConfigSet)
	}
	intent, err := normalizeConfigSet(req.ConfigSet)
	if err != nil {
		return nil, err
	}
	intentJSON, err := ledger.Canonical(intent)
	if err != nil {
		return nil, err
	}
	if len(intentJSON) > ledger.MaxIntentBytes {
		return nil, fleetErr(FleetCodeInputTooLarge, "the intent exceeds %d bytes", ledger.MaxIntentBytes)
	}
	// The body bound comes before any normalisation work, so a huge list is refused cheaply.
	bytes := 0
	for _, t := range req.DeviceTokens {
		bytes += len(t) + 1
	}
	if bytes > ledger.MaxRequestBytes {
		return nil, fleetErr(FleetCodeInputTooLarge, "the device list exceeds %d bytes", ledger.MaxRequestBytes)
	}
	tokens := normalizeFleetTokens(req.DeviceTokens)
	if len(tokens) == 0 {
		return nil, fleetErr(FleetCodeInvalidRequest, "at least one device token is required")
	}
	if len(tokens) > ledger.MaxLinesPerOperation {
		return nil, fleetErr(FleetCodeOperationTooLarge, "an operation may target at most %d devices, got %d", ledger.MaxLinesPerOperation, len(tokens))
	}
	safety, err := normalizeFleetSafety(req.Safety)
	if err != nil {
		return nil, err
	}
	digest, err := ledger.RequestDigest(ledger.RequestDigestInput{Kind: req.Kind, Intent: intent, TargetTokens: tokens, Safety: safety})
	if err != nil {
		return nil, err
	}
	return &fleetNormalized{kind: req.Kind, intent: intent, intentJSON: string(intentJSON), tokens: tokens, safety: safety, requestDigest: digest}, nil
}

func normalizeConfigSet(in *FleetConfigSetIntent) (FleetConfigSetIntent, error) {
	out := FleetConfigSetIntent{Key: strings.TrimSpace(in.Key), Unset: in.Unset}
	if out.Key == "" || len(out.Key) > 256 {
		return out, fleetErr(FleetCodeInvalidRequest, "configSet.key must be 1-256 characters")
	}
	if in.Unset {
		if in.Value != nil || in.ValueType != "" {
			return out, fleetErr(FleetCodeInvalidRequest, "an unset carries neither a value nor a valueType")
		}
		return out, nil
	}
	if !AttributeValueType(in.ValueType).Valid() {
		return out, fleetErr(FleetCodeInvalidRequest, "configSet.valueType %q is not a value type", in.ValueType)
	}
	if in.Value == nil {
		return out, fleetErr(FleetCodeInvalidRequest, "configSet.value is required unless it is an unset")
	}
	out.ValueType = in.ValueType
	v := *in.Value
	out.Value = &v
	return out, nil
}

// normalizeFleetTokens trims every token and drops later repeats, keeping the first occurrence's
// position so ordinals follow the operator's own input order.
func normalizeFleetTokens(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func normalizeFleetSafety(in FleetSafetyRequest) (ledger.Safety, error) {
	rate := int64(ledger.DefaultMaxPostsPerSecond)
	if in.MaxPostsPerSecond != nil {
		rate = int64(*in.MaxPostsPerSecond)
	}
	if rate < 1 || rate > ledger.TenantPostsPerSecond {
		return ledger.Safety{}, fleetErr(FleetCodeInvalidRequest, "maxPostsPerSecond must be 1-%d", ledger.TenantPostsPerSecond)
	}
	deadline := int64(ledger.DefaultSettleDeadlineSeconds)
	if in.SettleDeadlineSeconds != nil {
		deadline = int64(*in.SettleDeadlineSeconds)
	}
	if deadline < ledger.MinSettleDeadlineSeconds || deadline > ledger.MaxSettleDeadlineSeconds {
		return ledger.Safety{}, fleetErr(FleetCodeInvalidRequest, "settleDeadlineSeconds must be %d-%d", ledger.MinSettleDeadlineSeconds, ledger.MaxSettleDeadlineSeconds)
	}
	out := ledger.Safety{MaxPostsPerSecond: &rate, SettleDeadlineSeconds: deadline}
	for name, pair := range map[string]struct {
		in  *int32
		out **int64
	}{
		"failureThreshold":   {in.FailureThreshold, &out.FailureThreshold},
		"ambiguityThreshold": {in.AmbiguityThreshold, &out.AmbiguityThreshold},
	} {
		if pair.in == nil {
			continue
		}
		if *pair.in < 1 {
			return ledger.Safety{}, fleetErr(FleetCodeInvalidRequest, "%s must be at least 1 when given", name)
		}
		v := int64(*pair.in)
		*pair.out = &v
	}
	return out, nil
}

// PlanFleetOperation resolves the request's targets and returns the plan. It writes nothing and
// takes no lock. An unknown token, a token of another tenant and an empty token all become the
// same plan-time refusal, so the answer reveals nothing about other tenants.
func (api *Api) PlanFleetOperation(ctx context.Context, req *FleetOperationCreateRequest) (*FleetPlan, error) {
	n, err := normalizeFleetRequest(req)
	if err != nil {
		return nil, err
	}
	return api.planNormalized(ctx, n)
}

func (api *Api) planNormalized(ctx context.Context, n *fleetNormalized) (*FleetPlan, error) {
	ids := make(map[string]uint, len(n.tokens))
	for start := 0; start < len(n.tokens); start += rdb.MaxLookupKeys {
		chunk := n.tokens[start:min(start+rdb.MaxLookupKeys, len(n.tokens))]
		var rows []Device
		if err := rdb.FindByKeys(api.RDB.DB(ctx).Select("id", "token"), &rows, "token", chunk); err != nil {
			return nil, err
		}
		for _, d := range rows {
			ids[d.Token] = d.ID
		}
	}

	return buildFleetPlan(n, ids)
}

// buildFleetPlan turns the resolved tokens into lines and digests. It is pure: ids maps a token to
// the device it names, and a token missing from the map names none in this tenant.
func buildFleetPlan(n *fleetNormalized, ids map[string]uint) (*FleetPlan, error) {
	plan := &FleetPlan{Kind: n.kind, Intent: n.intentJSON, Safety: n.safety, RefusalCounts: map[string]int{}}
	taken := make(map[uint]struct{}, len(ids))
	digestLines := make([]ledger.PlanLine, 0, len(n.tokens))
	for i, token := range n.tokens {
		line := FleetPlanLine{Ordinal: int32(i + 1), Token: token}
		id, found := ids[token]
		switch {
		case !found:
			line.Code = FleetLineTargetNotFound
		default:
			if _, dup := taken[id]; dup {
				line.Code = FleetLineDuplicateTarget
			} else {
				taken[id] = struct{}{}
				v := id
				line.TargetId = &v
			}
		}
		dl := ledger.PlanLine{Ordinal: int64(line.Ordinal), Admission: "ADMITTED"}
		if line.TargetId != nil {
			t := int64(*line.TargetId)
			dl.TargetID = &t
		}
		if line.Code != "" {
			dl.Admission = line.Code
			plan.Refused++
			plan.RefusalCounts[line.Code]++
			if plan.RefusalCounts[line.Code] <= MaxFleetPlanRefusals {
				plan.Refusals = append(plan.Refusals, FleetPlanRefusal{Ordinal: line.Ordinal, Token: token, Code: line.Code})
			}
		} else {
			plan.Admitted++
		}
		digestLines = append(digestLines, dl)
		plan.Lines = append(plan.Lines, line)
	}
	plan.RequestDigest = n.requestDigest
	var err error
	if plan.PlanDigest, err = ledger.PlanDigest(ledger.PlanDigestInput{Lines: digestLines}); err != nil {
		return nil, err
	}
	contract := ledger.ContractDigestInput{V: 1, Kind: n.kind, Intent: n.intent, PlanDigest: plan.PlanDigest, Safety: n.safety,
		Approval: ledger.Approval{Kind: FleetApprovalHumanDirect, PolicyVersion: 1}}
	if plan.ContractDigest, err = ledger.ContractDigest(contract); err != nil {
		return nil, err
	}
	body, err := ledger.Canonical(contract)
	if err != nil {
		return nil, err
	}
	plan.ContractBody = string(body)
	return plan, nil
}

// FleetApprovalHumanDirect is the only approval kind this release writes.
const FleetApprovalHumanDirect = "HUMAN_DIRECT"

// intentFrom decodes a stored intent.
func fleetIntentFrom(s string) (*FleetConfigSetIntent, error) {
	var in FleetConfigSetIntent
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		return nil, err
	}
	return &in, nil
}
