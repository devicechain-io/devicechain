// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Alarm proposals are executed on final human approval after a fresh policy decision.
// Agent tokens can propose, but never directly acknowledge or clear alarms.

// LedgerScope separates this area's proposal counters from another area's (actiongate.LockCounters).
const LedgerScope = "device-management"

// AlarmProposalAction is what an alarm proposal asks for.
type AlarmProposalAction string

const (
	AlarmProposalAck   AlarmProposalAction = "ACK"
	AlarmProposalClear AlarmProposalAction = "CLEAR"
)

// kind is the ledger kind of the action, "" for an unknown one.
func (a AlarmProposalAction) kind() actiongate.Kind {
	switch a {
	case AlarmProposalAck:
		return actiongate.KindAlarmAck
	case AlarmProposalClear:
		return actiongate.KindAlarmClear
	}
	return ""
}

// Bounds of an alarm proposal.
const (
	MaxProposalAlarms     = 100
	MaxProposalRequestId  = 128
	MaxProposalRationale  = 2048
	MaxProposalRejectNote = 512
)

// Outcome statuses of a proposal request.
const (
	ProposalStatusDryRun          = "DRY_RUN"
	ProposalStatusPending         = "PENDING_APPROVAL"
	ProposalStatusRefused         = "REFUSED"
	ProposalStatusExisting        = "EXISTING"
	ReasonAlarmNotFound           = "ALARM_NOT_FOUND"
	ProposalImpactNormal          = "NORMAL"
	proposalDependencyRaisedTimes = "raisedTimes"
)

// AlarmProposalRequest is an agent's request to acknowledge or clear alarms.
type AlarmProposalRequest struct {
	Action      AlarmProposalAction
	AlarmTokens []string
	RequestId   string
	Rationale   *string
	DryRun      bool
}

// AlarmProposalPreview is what the platform computed about one target alarm, now: never the
// agent's account of it.
type AlarmProposalPreview struct {
	AlarmToken   string
	State        string
	Acknowledged bool
	// HidesLiveCondition is true for a CLEAR of an alarm whose condition is still active: clearing
	// it would hide a condition that is still true.
	HidesLiveCondition bool
}

// ProposalOutcome is the answer to a proposal request.
type ProposalOutcome struct {
	Status            string
	Proposal          *AgentProposal
	Verdict           actiongate.Verdict
	RequiredApprovals int
	Reasons           []string
	Digest            string
	ExpiresAt         *time.Time
	TargetCount       int
	Preview           []AlarmProposalPreview
}

// errAgentDecider is the refusal when the area has no way to ask the tenant's policy: a wiring
// fault, which refuses every proposal rather than deciding without the policy.
var errAgentDecider = actiongate.ErrPolicyUnavailable

// normalizeAlarmProposal validates and normalizes a request: tokens deduplicated and sorted, so
// the canonical form, and with it the digest, depends only on what is asked.
func normalizeAlarmProposal(req AlarmProposalRequest) (actiongate.Kind, []string, error) {
	kind := req.Action.kind()
	if kind == "" {
		return "", nil, fmt.Errorf("unknown alarm action %q", req.Action)
	}
	if n := len(req.RequestId); n == 0 || n > MaxProposalRequestId || !printable(req.RequestId) {
		return "", nil, fmt.Errorf("requestId must be 1 to %d printable characters", MaxProposalRequestId)
	}
	if req.Rationale != nil {
		if len(*req.Rationale) > MaxProposalRationale || !utf8.ValidString(*req.Rationale) || strings.ContainsRune(*req.Rationale, 0) {
			return "", nil, fmt.Errorf("rationale must be valid text of at most %d bytes", MaxProposalRationale)
		}
	}
	if len(req.AlarmTokens) == 0 || len(req.AlarmTokens) > MaxProposalAlarms {
		return "", nil, fmt.Errorf("a proposal names 1 to %d alarms", MaxProposalAlarms)
	}
	var tokens []string
	for i, t := range req.AlarmTokens {
		if err := core.ValidateToken(t); err != nil {
			return "", nil, fmt.Errorf("alarmTokens[%d]: %w", i, err)
		}
		if !slices.Contains(tokens, t) {
			tokens = append(tokens, t)
		}
	}
	slices.Sort(tokens)
	return kind, tokens, nil
}

func printable(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return utf8.ValidString(s)
}

// alarmProposalForm is the canonical action and target set of an alarm proposal.
type alarmProposalForm struct {
	payload, targets, dependencies []byte
	digest                         string
	preview                        []AlarmProposalPreview
	missing                        []string
}

// buildAlarmProposal reads the target alarms NOW, under the caller's own context, and builds the
// canonical form. An alarm that does not exist (or that the caller cannot see) is listed in
// missing.
func (api *Api) buildAlarmProposal(ctx context.Context, kind actiongate.Kind, action AlarmProposalAction, tokens []string) (*alarmProposalForm, error) {
	found, err := api.AlarmsByToken(ctx, tokens)
	if err != nil {
		return nil, err
	}
	byToken := map[string]*Alarm{}
	for _, a := range found {
		byToken[a.Token] = a
	}
	f := &alarmProposalForm{}
	// What the action's MEANING depends on: the alarm CYCLE it was asked about. An acknowledge
	// approved for one cycle must not land on the next one.
	raised := map[string]string{}
	for _, t := range tokens {
		a, ok := byToken[t]
		if !ok {
			f.missing = append(f.missing, t)
			continue
		}
		raised[t] = a.RaisedTime.UTC().Format(time.RFC3339Nano)
		f.preview = append(f.preview, AlarmProposalPreview{
			AlarmToken: t, State: a.State, Acknowledged: a.Acknowledged,
			HidesLiveCondition: action == AlarmProposalClear && a.State == string(AlarmStateActive),
		})
	}
	if f.payload, err = actiongate.Canonical(map[string]any{"action": string(action), "alarms": tokens}); err != nil {
		return nil, err
	}
	if f.targets, err = actiongate.Canonical(tokens); err != nil {
		return nil, err
	}
	if f.dependencies, err = actiongate.Canonical(map[string]any{proposalDependencyRaisedTimes: raised}); err != nil {
		return nil, err
	}
	f.digest = actiongate.Digest(kind, f.payload, f.targets)
	return f, nil
}

// ProposeAlarmAction records an agent's request to acknowledge or clear alarms, after the gate:
// the tenant's decision (asked under the agent's own token), the targets' validation, and the
// counters under their locks. proposer comes from actiongate.AuthorizeProposer, which the resolver
// has already applied.
//
// A dry run decides and validates but stores nothing. A request whose requestId this client and
// principal have used before returns that proposal when it asks for the same thing, and is a
// CONFLICT when it asks for something else.
func (api *Api) ProposeAlarmAction(ctx context.Context, proposer actiongate.Proposer, req AlarmProposalRequest) (result *ProposalOutcome, resultErr error) {
	defer func() {
		if !req.DryRun {
			api.AgentMetrics.proposal(req.Action.kind(), result, resultErr)
		}
	}()
	kind, tokens, err := normalizeAlarmProposal(req)
	if err != nil {
		return nil, err
	}
	form, err := api.buildAlarmProposal(ctx, kind, req.Action, tokens)
	if err != nil {
		return nil, err
	}
	if !req.DryRun {
		if prior, err := api.proposalByRequest(ctx, proposer, req.RequestId); err != nil {
			return nil, err
		} else if prior != nil {
			return existingOutcome(prior, form.digest)
		}
	}
	if api.AgentDecider == nil {
		return nil, errAgentDecider
	}
	// Tenant policy gates proposals; the local kill fence guards execution only.
	// This decision has no actuation generation, and a closed fence may await re-enable
	// by the next execution, so checking the row here would prevent that recovery.
	// Proposals change no alarms: a fenced approval leaves them pending for a later
	// approval with a newer generation, or until they expire.
	dec, err := api.AgentDecider.Actuation(ctx, actiongate.ActuationRequest{Kind: kind, Impact: ProposalImpactNormal, TargetCount: len(tokens)})
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return nil, err
		}
		return nil, actiongate.ErrPolicyUnavailable
	}
	out := &ProposalOutcome{
		Verdict: dec.Verdict, RequiredApprovals: dec.RequiredApprovals, Reasons: actiongate.PersistedReasons(dec.Reasons),
		Digest: form.digest, TargetCount: len(tokens), Preview: form.preview,
	}
	switch dec.Verdict {
	case actiongate.VerdictApprove:
	case actiongate.VerdictAuto:
		// No automatic path in this release: an AUTO verdict is a refusal here, whatever answered it.
		out.Verdict, out.RequiredApprovals = actiongate.VerdictRefuse, 0
		out.Reasons = append(out.Reasons, actiongate.ReasonAutoNotAvailable)
	default:
		out.Verdict, out.RequiredApprovals = actiongate.VerdictRefuse, 0
	}
	if len(form.missing) > 0 && out.Verdict != actiongate.VerdictRefuse {
		out.Verdict, out.RequiredApprovals = actiongate.VerdictRefuse, 0
		out.Reasons = append(out.Reasons, ReasonAlarmNotFound)
	}
	now := time.Now().UTC()
	if req.DryRun {
		out.Status = ProposalStatusDryRun
		if out.Verdict == actiongate.VerdictApprove {
			exp := now.Add(dec.Expiry)
			out.ExpiresAt = &exp
		}
		return out, nil
	}

	row, err := api.newProposalRow(proposer, kind, req, form, dec, out, now)
	if err != nil {
		return nil, err
	}
	var refusedByCounter string
	var existing *AgentProposal
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := actiongate.LockCounters(tx, LedgerScope, proposer.Tenant, proposer.Client, proposer.Principal); err != nil {
			return err
		}
		// Two identical retries can both miss the lookup before the locks; the second must find
		// the first row under the client lock, not count against the limit the first just took.
		var prior []AgentProposal
		if err := tx.Where("proposer_client = ? AND proposer_sub = ? AND request_id = ?", proposer.Client, proposer.Principal, req.RequestId).Limit(1).Find(&prior).Error; err != nil {
			return err
		}
		if len(prior) > 0 {
			existing = &prior[0]
			return nil
		}
		counts, err := countProposals(tx, proposer, now)
		if err != nil {
			return err
		}
		if reason := actiongate.LimitsFor(dec.ActionsPerHour).Refusal(counts); reason != "" {
			// Not stored: a flood held at the limit must not grow the ledger without bound.
			refusedByCounter = reason
			return nil
		}
		return tx.Create(row).Error
	})
	if err != nil {
		if conflict.Is(err) {
			// A racing call with the same requestId landed first.
			prior, perr := api.proposalByRequest(ctx, proposer, req.RequestId)
			if perr == nil && prior != nil {
				return existingOutcome(prior, form.digest)
			}
		}
		return nil, err
	}
	if existing != nil {
		return existingOutcome(existing, form.digest)
	}
	if refusedByCounter != "" {
		out.Status, out.Verdict, out.RequiredApprovals = ProposalStatusRefused, actiongate.VerdictRefuse, 0
		out.Reasons = append(out.Reasons, refusedByCounter)
		return out, nil
	}
	out.Proposal = row
	if row.State == string(actiongate.StateProposed) {
		out.Status = ProposalStatusPending
		out.ExpiresAt = &row.ExpiresAt
	} else {
		out.Status = ProposalStatusRefused
	}
	return out, nil
}

// newProposalRow builds the ledger row of a decided proposal: PROPOSED when it awaits approval,
// REFUSED (stored for the record, expired at once) otherwise.
func (api *Api) newProposalRow(proposer actiongate.Proposer, kind actiongate.Kind, req AlarmProposalRequest,
	form *alarmProposalForm, dec actiongate.ActuationDecision, out *ProposalOutcome, now time.Time) (*AgentProposal, error) {
	token, err := actiongate.NewToken()
	if err != nil {
		return nil, err
	}
	effect, err := actiongate.NewToken()
	if err != nil {
		return nil, err
	}
	reasons, err := json.Marshal(actiongate.PersistedReasons(out.Reasons))
	if err != nil {
		return nil, err
	}
	row := &AgentProposal{Model: gorm.Model{CreatedAt: now, UpdatedAt: now}, Record: actiongate.Record{
		Token: token, RequestId: req.RequestId, EffectToken: effect, Kind: string(kind),
		State: string(actiongate.StateProposed), Payload: datatypes.JSON(form.payload),
		TargetSet: datatypes.JSON(form.targets), Dependencies: datatypes.JSON(form.dependencies),
		Digest: form.digest, Impact: ProposalImpactNormal, TargetCount: out.TargetCount,
		ProposerSub: proposer.Principal, ProposerClient: proposer.Client, ClientAttested: dec.ClientAttested,
		Rationale: req.Rationale, PolicyVersion: dec.PolicyVersion, RequiredApprovals: out.RequiredApprovals,
		Approvals: datatypes.JSON("[]"), Reasons: datatypes.JSON(reasons), ExpiresAt: now.Add(dec.Expiry),
	}}
	if proposer.Sid != "" {
		sid := proposer.Sid
		row.ProposerSid = &sid
	}
	if out.Verdict != actiongate.VerdictApprove {
		row.State = string(actiongate.StateRefused)
		row.ExpiresAt = now
		row.DecidedAt = &now
	}
	return row, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// countProposals reads the counters for proposer inside the locked transaction.
func countProposals(tx *gorm.DB, proposer actiongate.Proposer, now time.Time) (actiongate.Counts, error) {
	var c actiongate.Counts
	since := now.Add(-actiongate.CounterWindow)
	if err := tx.Model(&AgentProposal{}).Where("proposer_client = ? AND created_at > ?", proposer.Client, since).
		Count(&c.ClientHour).Error; err != nil {
		return c, err
	}
	if err := tx.Model(&AgentProposal{}).Where("proposer_sub = ? AND created_at > ?", proposer.Principal, since).
		Count(&c.PrincipalHour).Error; err != nil {
		return c, err
	}
	err := tx.Model(&AgentProposal{}).Where("proposer_client = ? AND state = ? AND expires_at > ?",
		proposer.Client, string(actiongate.StateProposed), now).Count(&c.ClientOpen).Error
	return c, err
}

// proposalByRequest finds the proposal this client and principal made under requestId, or nil.
func (api *Api) proposalByRequest(ctx context.Context, proposer actiongate.Proposer, requestId string) (*AgentProposal, error) {
	var found []AgentProposal
	if err := api.RDB.DB(ctx).Where("proposer_client = ? AND proposer_sub = ? AND request_id = ?",
		proposer.Client, proposer.Principal, requestId).Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &found[0], nil
}

// existingOutcome answers a repeated requestId: the original proposal when it asked for the same
// thing, a CONFLICT otherwise. It never answers the original row for a different request.
func existingOutcome(prior *AgentProposal, digest string) (*ProposalOutcome, error) {
	if !actiongate.DigestEqual(prior.Digest, digest) {
		return nil, conflict.New("this requestId was already used for a different proposal")
	}
	out := &ProposalOutcome{
		Status: ProposalStatusExisting, Proposal: prior, Digest: prior.Digest, TargetCount: prior.TargetCount,
		RequiredApprovals: prior.RequiredApprovals, Verdict: actiongate.VerdictApprove,
	}
	if prior.State == string(actiongate.StateRefused) {
		out.Verdict, out.RequiredApprovals = actiongate.VerdictRefuse, 0
	}
	if err := json.Unmarshal(prior.Reasons, &out.Reasons); err != nil {
		out.Reasons = []string{}
	}
	out.Reasons = actiongate.PersistedReasons(out.Reasons)
	if prior.State == string(actiongate.StateProposed) {
		exp := prior.ExpiresAt
		out.ExpiresAt = &exp
	}
	return out, nil
}

// kindWrite is the raw write an approver of each kind must hold.
var kindWrite = map[actiongate.Kind]auth.Authority{
	actiongate.KindAlarmAck:   auth.AlarmWrite,
	actiongate.KindAlarmClear: auth.AlarmWrite,
}

// ProposalWriteAuthority is the raw write an approver of a proposal of kind must hold.
func ProposalWriteAuthority(kind string) (auth.Authority, bool) {
	w, ok := kindWrite[actiongate.Kind(kind)]
	return w, ok
}

// AgentProposalByToken reads one proposal of the tenant. viewer, when non-nil, restricts it to
// that agent's own proposals.
func (api *Api) AgentProposalByToken(ctx context.Context, token string, viewer *actiongate.Proposer) (*AgentProposal, error) {
	q := api.RDB.DB(ctx).Where("token = ?", token)
	if viewer != nil {
		q = q.Where("proposer_client = ? AND proposer_sub = ?", viewer.Client, viewer.Principal)
	}
	var found []AgentProposal
	if err := q.Limit(1).Find(&found).Error; err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &found[0], nil
}

// AgentProposalSearchCriteria filters the ledger.
type AgentProposalSearchCriteria struct {
	rdb.Pagination
	State *string
	Kind  *string
}

// AgentProposalSearchResults is one page of the ledger.
type AgentProposalSearchResults struct {
	Results    []AgentProposal
	Pagination rdb.SearchResultsPagination
}

// AgentProposals pages the ledger, newest first. viewer, when non-nil, restricts it to that
// agent's own proposals.
func (api *Api) AgentProposals(ctx context.Context, criteria AgentProposalSearchCriteria, viewer *actiongate.Proposer) (*AgentProposalSearchResults, error) {
	if criteria.State != nil && !actiongate.State(*criteria.State).Valid() {
		return nil, fmt.Errorf("unknown proposal state %q", *criteria.State)
	}
	if criteria.Kind != nil && !actiongate.Kind(*criteria.Kind).Valid() {
		return nil, fmt.Errorf("unknown proposal kind %q", *criteria.Kind)
	}
	results := make([]AgentProposal, 0)
	db, pag := api.RDB.ListOf(ctx, &AgentProposal{}, func(q *gorm.DB) *gorm.DB {
		if viewer != nil {
			q = q.Where("agent_proposals.proposer_client = ? AND agent_proposals.proposer_sub = ?", viewer.Client, viewer.Principal)
		}
		if criteria.State != nil {
			q = q.Where("agent_proposals.state = ?", *criteria.State)
		}
		if criteria.Kind != nil {
			q = q.Where("agent_proposals.kind = ?", *criteria.Kind)
		}
		return q
	}, criteria.Pagination)
	db.Find(&results)
	if db.Error != nil {
		return nil, db.Error
	}
	return &AgentProposalSearchResults{Results: results, Pagination: pag}, nil
}

// pendingProposal reads a proposal for a person's decision and checks it is still decidable: still
// PROPOSED and inside its expiry.
func (api *Api) pendingProposal(ctx context.Context, token string, now time.Time) (*AgentProposal, error) {
	p, err := api.AgentProposalByToken(ctx, token, nil)
	if err != nil {
		return nil, err
	}
	if p.State != string(actiongate.StateProposed) {
		return nil, actiongate.ErrNotPending
	}
	if !p.ExpiresAt.After(now) {
		return nil, errProposalExpired
	}
	return p, nil
}

// The refusals of a person's decision.
var (
	errProposalExpired = actiongate.Refusal(actiongate.CodeExpired, "the proposal has expired", false)
	// ErrProposalDigestMismatch refuses an approval of anything but the stored action.
	ErrProposalDigestMismatch = actiongate.Refusal(actiongate.CodeDigestMismatch,
		"the digest does not match the proposal: what you were shown is not what is stored; reload it and review it again", false)
	errNotAnApprover = actiongate.Refusal(actiongate.CodeNotAnApprover,
		"you are not an approver of this proposal: approvers hold an approver role in the agent policy, the write the action needs, and an active second factor", true)
	errSelfApproval = actiongate.Refusal(actiongate.CodeSelfApproval,
		"the person an agent acted for cannot approve its proposal here: it needs another person", true)
	errConcurrentApproval = actiongate.Refusal(actiongate.CodeConcurrentApproval,
		"another decision on this proposal landed at the same time; reload it and try again", false)
)

// ApproveAgentProposal records approver's approval of the proposal token, bound to digest.
//
// The order is the contract:
//
//  1. the proposal must still be PROPOSED, inside its expiry, and its stored digest must equal the
//     one the approver was shown;
//  2. collect previous approvers with their stored sign-ins and the caller with their current sign-in;
//  3. user-management is asked, under the APPROVER'S own token and with no cache, whether the
//     proposal may still go ahead (agentExecutionDecision): the original client, the proposer's
//     standing and every counted approver's standing, under the current policy. A refusal ends
//     the proposal as REFUSED with the reasons;
//  4. an already-approved, still-valid approver with a fresh approval from the SAME sign-in is answered unchanged;
//     otherwise their stale entry is replaced after the normal checks. The approver must be a
//     valid approver, with a second factor fresh by the policy's measure, and may be the proposer
//     only when the policy allows self-confirmation of a
//     single-approval, normal-impact proposal;
//  5. the final approval executes atomically with the decision asked again as the last act
//     before commit; earlier approvals are recorded with a compare-and-set on the row's revision.
func (api *Api) ApproveAgentProposal(ctx context.Context, approver actiongate.Approver, token, digest string) (result *AgentProposal, resultErr error) {
	defer func() { api.AgentMetrics.approval(result, resultErr) }()
	for attempt := 0; ; attempt++ {
		now := time.Now().UTC()
		p, err := api.pendingProposal(ctx, token, now)
		if err != nil {
			return nil, err
		}
		if !actiongate.DigestEqual(p.Digest, digest) {
			return nil, ErrProposalDigestMismatch
		}
		approvals, err := actiongate.ParseApprovals(p.Approvals)
		if err != nil {
			return nil, err
		}
		sessions := []actiongate.ApproverSession{}
		for _, s := range actiongate.ApproverSessions(approvals) {
			if s.Principal != approver.Principal {
				sessions = append(sessions, s)
			}
		}
		if approver.Sid != "" {
			sessions = append(sessions, actiongate.ApproverSession{Principal: approver.Principal, Sid: approver.Sid})
		}
		principals := actiongate.Principals(approvals)
		if !slices.Contains(principals, approver.Principal) {
			principals = append(principals, approver.Principal)
		}
		if api.AgentDecider == nil {
			return nil, errAgentDecider
		}
		sid := ""
		if p.ProposerSid != nil {
			sid = *p.ProposerSid
		}
		dec, err := api.AgentDecider.Execution(ctx, actiongate.ExecutionRequest{
			ProposalToken: p.Token, Kind: actiongate.Kind(p.Kind), Impact: p.Impact, TargetCount: p.TargetCount,
			ProposerPrincipal: p.ProposerSub, ProposerClient: p.ProposerClient, ProposerSid: sid,
			Approvers: principals, ApproverSessions: sessions,
		})
		// The decision is a remote call, so the window and step-up are judged when the approval
		// is written, not when the request arrived.
		now = time.Now().UTC()
		if !p.ExpiresAt.After(now) {
			return nil, errProposalExpired
		}
		if err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return nil, err
			}
			return nil, actiongate.ErrPolicyUnavailable
		}
		if dec.Verdict != actiongate.VerdictApprove {
			reasons := actiongate.PersistedReasons(dec.Reasons)
			if dec.Verdict == actiongate.VerdictAuto {
				reasons = append(reasons, actiongate.ReasonAutoNotAvailable)
			}
			return api.endProposal(ctx, p.Token, actiongate.StateRefused, approver.Principal, nil, reasons, now)
		}
		if !slices.Contains(dec.ValidApprovers, approver.Principal) {
			return nil, errNotAnApprover
		}
		for _, a := range approvals {
			if a.Sub == approver.Principal && auth.SameSession(auth.SessionID(a.Sid), auth.SessionID(approver.Sid)) && auth.StepUpFreshAt(a.Amr, a.MfaTime, dec.StepUp, now) {
				// Nothing to write; answer what is stored NOW, not the row read before the remote
				// decision, which a concurrent decision may have moved on.
				return api.AgentProposalByToken(ctx, p.Token, nil)
			}
		}
		if err := actiongate.CheckStepUp(approver, dec.StepUp, now); err != nil {
			return nil, err
		}
		if approver.Principal == p.ProposerSub && (!dec.AllowSelfConfirm || dec.RequiredApprovals > 1) {
			return nil, errSelfApproval
		}
		next := make([]actiongate.Approval, 0, len(approvals)+1)
		for _, a := range approvals {
			if a.Sub != approver.Principal {
				next = append(next, a)
			}
		}
		next = append(next, actiongate.Approval{Sub: approver.Principal, Sid: approver.Sid, Amr: approver.Amr, MfaTime: approver.MfaTime, At: now})
		if actiongate.Tally(next, dec.ValidApprovers, p.ProposerSub, dec.RequiredApprovals, dec.AllowSelfConfirm, dec.StepUp, now) >= dec.RequiredApprovals {
			result, execErr := api.executeAlarmProposal(ctx, approver, p, next, dec.RequiredApprovals, attempt == 0)
			if errors.Is(execErr, actiongate.ErrNotPending) {
				if attempt == 0 {
					continue
				}
				return nil, errConcurrentApproval
			}
			return result, execErr
		}
		err = actiongate.RecordApproval(api.RDB.DB(ctx), &AgentProposal{}, p.Token, p.Revision, next, dec.RequiredApprovals, now)
		if errors.Is(err, actiongate.ErrNotPending) && attempt == 0 {
			// Another decision landed first: decide again on what is there now.
			continue
		}
		if errors.Is(err, actiongate.ErrNotPending) {
			return nil, errConcurrentApproval
		}
		if err != nil {
			return nil, err
		}
		return api.AgentProposalByToken(ctx, p.Token, nil)
	}
}

// RejectAgentProposal ends the proposal token as REJECTED by person, with an optional note. A
// rejection only stops something, so it needs no step-up and no policy read: any person who
// could approve the kind may stop it.
func (api *Api) RejectAgentProposal(ctx context.Context, person actiongate.Approver, token string, note *string) (*AgentProposal, error) {
	if note != nil && (len(*note) > MaxProposalRejectNote || !utf8.ValidString(*note) || strings.ContainsRune(*note, 0)) {
		return nil, fmt.Errorf("note must be valid text of at most %d bytes", MaxProposalRejectNote)
	}
	now := time.Now().UTC()
	p, err := api.pendingProposal(ctx, token, now)
	if err != nil {
		return nil, err
	}
	return api.endProposal(ctx, p.Token, actiongate.StateRejected, person.Principal, note, nil, now)
}

// endProposal moves a PROPOSED proposal to a terminal state through the one conditional
// transition, and returns it.
func (api *Api) endProposal(ctx context.Context, token string, to actiongate.State, by string, note *string, reasons []string, now time.Time) (*AgentProposal, error) {
	set := map[string]any{"decided_at": now, "decided_by": by}
	if note != nil {
		set["note"] = *note
	}
	if reasons != nil {
		raw, err := json.Marshal(actiongate.PersistedReasons(reasons))
		if err != nil {
			return nil, err
		}
		set["reasons"] = datatypes.JSON(raw)
	}
	if err := actiongate.Transition(api.RDB.DB(ctx), &AgentProposal{}, token, actiongate.StateProposed, to, now, set); err != nil {
		return nil, err
	}
	return api.AgentProposalByToken(ctx, token, nil)
}

// ExpireAgentProposals moves every overdue PROPOSED proposal of every tenant to EXPIRED (see
// actiongate.ExpireDue).
//
// 🔴 A SYSTEM CONTEXT, ON PURPOSE: the reaper is one conditional statement across tenants that can
// only move a PROPOSED row whose expiry has passed to EXPIRED. It reads nothing back, writes no
// tenant's data but that one state, and can only STOP a proposal, never start or approve one.
func (api *Api) ExpireAgentProposals(ctx context.Context) (int64, error) {
	n, err := actiongate.ExpireDue(core.WithSystemContext(ctx), api.RDB.Database, &AgentProposal{}, time.Now().UTC())
	if err == nil && api.AgentMetrics != nil {
		api.AgentMetrics.expired.Add(float64(n))
	}
	return n, err
}
