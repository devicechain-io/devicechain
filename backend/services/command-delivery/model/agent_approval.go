// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var errAgentDecider = actiongate.ErrPolicyUnavailable

// kindWrite is the raw write an approver of each kind must hold.
var kindWrite = map[actiongate.Kind]auth.Authority{
	actiongate.KindCommandSend: auth.CommandWrite,
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
	if criteria.Kind != nil && !commandProposalKind(*criteria.Kind) {
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
	if !api.CommandProposalsEnabled {
		return nil, errCommandProposalsDisabled
	}
	for attempt := 0; ; attempt++ {
		now := time.Now().UTC()
		p, err := api.pendingProposal(ctx, token, now)
		if errors.Is(err, errProposalExpired) {
			p, readErr := api.AgentProposalByToken(ctx, token, nil)
			if readErr != nil {
				return nil, readErr
			}
			if !actiongate.DigestEqual(p.Digest, digest) {
				return nil, ErrProposalDigestMismatch
			}
			form, verifyErr := verifyCommandProposal(p)
			if verifyErr != nil {
				return nil, verifyErr
			}
			cutoff, _ := time.Parse(time.RFC3339, form.Effective.Cutoff)
			if !api.agentNow().Before(cutoff) {
				return api.refuseCommandCutoff(ctx, p, approver.Principal)
			}
		}
		if err != nil {
			return nil, err
		}
		if !actiongate.DigestEqual(p.Digest, digest) {
			return nil, ErrProposalDigestMismatch
		}
		form, err := verifyCommandProposal(p)
		if err != nil {
			return nil, err
		}
		cutoff, _ := time.Parse(time.RFC3339, form.Effective.Cutoff)
		if !api.agentNow().Before(cutoff) {
			return api.refuseCommandCutoff(ctx, p, approver.Principal)
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
			Command: form.Intent.Command, ProposalToken: p.Token, Kind: actiongate.Kind(p.Kind), Impact: p.Impact, TargetCount: p.TargetCount,
			ProposerPrincipal: p.ProposerSub, ProposerClient: p.ProposerClient, ProposerSid: sid,
			Approvers: principals, ApproverSessions: sessions,
		})
		// The decision is a remote call, so the window and step-up are judged when the approval
		// is written, not when the request arrived.
		now = time.Now().UTC()
		if !api.agentNow().Before(cutoff) {
			return api.refuseCommandCutoff(ctx, p, approver.Principal)
		}
		if !p.ExpiresAt.After(now) {
			return nil, errProposalExpired
		}
		if err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return nil, err
			}
			return nil, actiongate.ErrPolicyUnavailable
		}
		if reason := commandDecisionRefusal(dec.Verdict, dec.GroupRestriction, p.Impact, dec.RequiredApprovals); reason != "" {
			return api.endProposal(ctx, p.Token, actiongate.StateRefused, approver.Principal, nil, append(actiongate.PersistedReasons(dec.Reasons), reason), now)
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
		if approver.Principal == p.ProposerSub && (!dec.AllowSelfConfirm || dec.RequiredApprovals > 1 || p.Impact != "NORMAL") {
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
			result, execErr := api.executeCommandProposal(ctx, approver, p, next, dec.RequiredApprovals, attempt == 0)
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
	return n, err
}

// ProposalRetention is how long a decided proposal, and with it its request key, is kept
// (ruled: 30 days). Inside it a retried requestId answers the original proposal; after it
// the key is free again, so an agent's requestId must not be reused within it for a
// different intent. The command a proposal admitted keeps its own proposal link and cause,
// which live as long as the command does.
const ProposalRetention = 30 * 24 * time.Hour

// proposalPurgeBatch bounds one purge statement; the ticker comes back for the rest.
const proposalPurgeBatch = 5000

// PurgeAgentProposals deletes, across tenants, up to one batch of proposals DECIDED more than
// ProposalRetention ago, and returns how many went. The clock starts at the decision, not at
// creation: every terminal transition (approval, refusal, rejection, expiry, kill, cutoff)
// stamps decided_at, so a late decision, or a proposal the reaper expires only after an
// outage, still keeps its request key for the full retention.
//
// 🔴 A SYSTEM CONTEXT, ON PURPOSE, as ExpireAgentProposals: one conditional statement that can
// only remove a row no decision can touch again (never a PROPOSED one, whatever its age: the
// reaper ends those first). It is a hard delete because retention means the row is gone:
// the request-key index is partial (WHERE deleted_at IS NULL), so a soft delete would also
// free the key, but it would keep the proposal's payload, rationale and identities in the
// table past the retention it is meant to end. Idempotent and HA-safe: two replicas
// deleting at once each remove what the other has not.
func (api *Api) PurgeAgentProposals(ctx context.Context) (int64, error) {
	db := api.RDB.Database.WithContext(core.WithSystemContext(ctx))
	due := db.Unscoped().Model(&AgentProposal{}).Select("id").
		Where("state <> ? AND decided_at IS NOT NULL AND decided_at < ?", string(actiongate.StateProposed), time.Now().UTC().Add(-ProposalRetention)).
		Order("id").Limit(proposalPurgeBatch)
	res := db.Unscoped().Where("id IN (?)", due).Delete(&AgentProposal{})
	return res.RowsAffected, res.Error
}

// commandProposalKind is this area's vocabulary; command proposals belong to command-delivery.
func commandProposalKind(kind string) bool {
	return kind == string(actiongate.KindCommandSend)
}
