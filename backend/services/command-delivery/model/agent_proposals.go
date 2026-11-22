// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var errCommandProposalsDisabled = actiongate.Refusal("COMMAND_PROPOSALS_DISABLED", "agent command proposals are not enabled on this instance", false)
var errValidationUnavailable = func() *actiongate.Error {
	e := actiongate.Refusal("VALIDATION_UNAVAILABLE", "command proposal validation is unavailable", false)
	e.Retryable = true
	return e
}()

func targetRefusal(t *ProposalTarget) string {
	switch {
	case !t.Exists:
		return "DEVICE_NOT_FOUND"
	case !t.Constrained:
		return "OPEN_VOCABULARY_NOT_SUPPORTED"
	case !t.Found:
		return "COMMAND_NOT_IN_VOCABULARY"
	case !t.Allowed:
		if t.RejectCode != "" {
			return t.RejectCode
		}
		return "PAYLOAD_REJECTED"
	case t.ScheduleRefusal != nil:
		if t.ScheduleRefusal.Code != "" {
			return string(t.ScheduleRefusal.Code)
		}
		return "SCHEDULE_REFUSED"
	case t.Impact != "NORMAL" && t.Impact != "DISRUPTIVE" && t.Impact != "DESTRUCTIVE":
		return "IMPACT_UNKNOWN"
	}
	return ""
}
func commandDecisionRefusal(v actiongate.Verdict, groups []string, impact string, required int) string {
	if v == actiongate.VerdictAuto {
		return actiongate.ReasonAutoNotAvailable
	}
	if groups != nil {
		return "GROUP_RESTRICTION_NOT_SUPPORTED"
	}
	if impact == "DESTRUCTIVE" && required < 2 {
		return "DESTRUCTIVE_NEEDS_TWO_APPROVERS"
	}
	if v == actiongate.VerdictApprove && required < 1 {
		return "HUMAN_APPROVAL_REQUIRED"
	}
	return ""
}
func (api *Api) readProposalTarget(ctx context.Context, i commandIntent, cutoff time.Time) (*ProposalTarget, error) {
	if api.ProposalTargets == nil {
		return nil, errValidationUnavailable
	}
	var payload []byte
	if i.PayloadPresent {
		payload = i.Payload
	}
	t, err := api.ProposalTargets.ReadProposalTarget(ctx, i.DeviceToken, i.Command, payload, cutoff)
	if err != nil {
		// A refusal of the caller's own authority, or a coded refusal, is the answer. Anything
		// else (a network or HTTP failure, an oversized or incomplete upstream answer) means the
		// facts could not be read: a RETRYABLE VALIDATION_UNAVAILABLE, never a verdict, and never
		// an uncoded error the client cannot tell from a defect.
		var coded *actiongate.Error
		var rejected *EnqueueRejected
		if errors.Is(err, auth.ErrForbidden) || errors.As(err, &coded) || errors.As(err, &rejected) {
			return nil, err
		}
		log.Warn().Err(err).Msg("Agent command proposal: device facts could not be read")
		return nil, errValidationUnavailable
	}
	if t == nil {
		return nil, errValidationUnavailable
	}
	return t, nil
}

// ProposeCommand uses only the caller-token target reader. Direct enqueue's validator,
// scheduler, pull-channel and window-opening seams present service credentials.
func (api *Api) ProposeCommand(ctx context.Context, proposer actiongate.Proposer, req CommandProposalRequest) (*CommandProposalOutcome, error) {
	if !api.CommandProposalsEnabled {
		return nil, errCommandProposalsDisabled
	}
	i, err := normalizeCommandIntent(req)
	if err != nil {
		return nil, err
	}
	// Submitted intent is compared before defaults, cutoffs, policy or target snapshots
	// are refreshed. In particular an old explicit cutoff remains replayable.
	if !req.DryRun {
		prior, err := api.proposalByRequest(ctx, proposer, req.RequestId)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			return existingCommandOutcome(prior, i)
		}
	}
	now := api.agentNow()
	if req.DryRun && !api.dryRunLimit().allow(now, "client|"+proposer.Tenant+"|"+proposer.Client, "principal|"+proposer.Tenant+"|"+proposer.Principal) {
		return nil, errDryRunRateExceeded()
	}
	cutoff, err := commandCutoff(i, now)
	if err != nil {
		return nil, err
	}
	if api.AgentDecider == nil {
		return nil, actiongate.ErrPolicyUnavailable
	}
	target, err := api.readProposalTarget(ctx, i, cutoff)
	if err != nil {
		return nil, err
	}
	dec := actiongate.ActuationDecision{Verdict: actiongate.VerdictRefuse}
	reason := targetRefusal(target)
	if reason == "" {
		dec, err = api.AgentDecider.Actuation(ctx, actiongate.ActuationRequest{Kind: actiongate.KindCommandSend, Impact: target.Impact, TargetCount: 1, Command: i.Command})
		if err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return nil, err
			}
			return nil, actiongate.ErrPolicyUnavailable
		}
		reason = commandDecisionRefusal(dec.Verdict, dec.GroupRestriction, target.Impact, dec.RequiredApprovals)
	}
	f := commandForm{V: 1, Intent: i, Effective: commandEffective{Cutoff: cutoff.Format(time.RFC3339), commandDependencies: commandDependencies{Incarnation: strconv.FormatUint(target.Incarnation, 10), Definition: target.DefinitionFingerprint, Impact: target.Impact}}}
	payload, targets, deps, digest, err := f.canonical()
	if err != nil {
		return nil, err
	}
	out := &CommandProposalOutcome{Verdict: dec.Verdict, RequiredApprovals: dec.RequiredApprovals, Reasons: actiongate.PersistedReasons(dec.Reasons), Digest: digest, TargetCount: 1, Preview: f.preview(target.Hold)}
	if reason != "" {
		out.Reasons = append(out.Reasons, reason)
	}
	if reason != "" || dec.Verdict != actiongate.VerdictApprove {
		out.Verdict = actiongate.VerdictRefuse
		out.RequiredApprovals = 0
	}
	exp := minTime(now.Add(actiongate.BoundExpiry(dec.Expiry)), cutoff)
	if req.DryRun {
		out.Status = ProposalStatusDryRun
		if out.Verdict == actiongate.VerdictApprove {
			out.ExpiresAt = &exp
		}
		return out, nil
	}
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
	row := &AgentProposal{Model: gorm.Model{CreatedAt: now, UpdatedAt: now}, Record: actiongate.Record{Token: token, EffectToken: effect, RequestId: req.RequestId, Kind: string(actiongate.KindCommandSend), State: string(actiongate.StateProposed), Payload: datatypes.JSON(payload), TargetSet: datatypes.JSON(targets), Dependencies: datatypes.JSON(deps), Digest: digest, Impact: target.Impact, TargetCount: 1, ProposerSub: proposer.Principal, ProposerClient: proposer.Client, ClientAttested: dec.ClientAttested, Rationale: req.Rationale, PolicyVersion: dec.PolicyVersion, RequiredApprovals: out.RequiredApprovals, Approvals: datatypes.JSON("[]"), Reasons: datatypes.JSON(reasons), ExpiresAt: exp}}
	if proposer.Sid != "" {
		sid := proposer.Sid
		row.ProposerSid = &sid
	}
	if out.Verdict != actiongate.VerdictApprove {
		row.State = string(actiongate.StateRefused)
		row.ExpiresAt = now
		row.DecidedAt = &now
	}
	var existing *AgentProposal
	var counterReason string
	err = api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCommandCounters(tx, proposer); err != nil {
			return err
		}
		// Re-probe under client -> principal -> tenant locks before charging.
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
		counterReason = actiongate.LimitsFor(dec.ActionsPerHour).Refusal(counts)
		if counterReason == "" {
			counterReason, err = tenantProposalRefusal(tx, now)
			if err != nil {
				return err
			}
		}
		if counterReason != "" {
			return nil
		}
		return tx.Create(row).Error
	})
	if err != nil {
		if proposalUniqueViolation(err) {
			prior, readErr := api.proposalByRequest(ctx, proposer, req.RequestId)
			if readErr == nil && prior != nil {
				return existingCommandOutcome(prior, i)
			}
		}
		return nil, err
	}
	if existing != nil {
		return existingCommandOutcome(existing, i)
	}
	if counterReason != "" {
		out.Status = ProposalStatusRefused
		out.Verdict = actiongate.VerdictRefuse
		out.RequiredApprovals = 0
		out.Reasons = append(out.Reasons, counterReason)
		return out, nil
	}
	out.Proposal = row
	out.Status = ProposalStatusRefused
	if row.State == string(actiongate.StateProposed) {
		out.Status = ProposalStatusPending
		out.ExpiresAt = &row.ExpiresAt
	}
	return out, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Every counter caller acquires locks in the fixed order client -> principal ->
// tenant. The tenant lock closes multiplication across different clients/people.
func lockCommandCounters(tx *gorm.DB, p actiongate.Proposer) error {
	if err := actiongate.LockCounters(tx, LedgerScope, p.Tenant, p.Client, p.Principal); err != nil {
		return err
	}
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "agent-proposals/command-delivery/tenant/"+p.Tenant).Error
	}
	return nil
}
func countProposals(tx *gorm.DB, p actiongate.Proposer, now time.Time) (actiongate.Counts, error) {
	var c actiongate.Counts
	since := now.Add(-actiongate.CounterWindow)
	if err := tx.Model(&AgentProposal{}).Where("proposer_client = ? AND created_at > ?", p.Client, since).Count(&c.ClientHour).Error; err != nil {
		return c, err
	}
	if err := tx.Model(&AgentProposal{}).Where("proposer_sub = ? AND created_at > ?", p.Principal, since).Count(&c.PrincipalHour).Error; err != nil {
		return c, err
	}
	err := tx.Model(&AgentProposal{}).Where("proposer_client = ? AND state = ? AND expires_at > ?", p.Client, string(actiongate.StateProposed), now).Count(&c.ClientOpen).Error
	return c, err
}
func tenantProposalRefusal(tx *gorm.DB, now time.Time) (string, error) {
	var open, hour int64
	if err := tx.Model(&AgentProposal{}).Where("state = ?", string(actiongate.StateProposed)).Count(&open).Error; err != nil {
		return "", err
	}
	if open >= TenantOpenProposals {
		return "TENANT_OPEN_PROPOSALS_EXCEEDED", nil
	}
	if err := tx.Model(&AgentProposal{}).Where("created_at > ?", now.Add(-time.Hour)).Count(&hour).Error; err != nil {
		return "", err
	}
	if hour >= TenantProposalsPerHour {
		return "TENANT_RATE_EXCEEDED", nil
	}
	return "", nil
}
func (api *Api) proposalByRequest(ctx context.Context, p actiongate.Proposer, id string) (*AgentProposal, error) {
	var rows []AgentProposal
	err := api.RDB.DB(ctx).Where("proposer_client = ? AND proposer_sub = ? AND request_id = ?", p.Client, p.Principal, id).Limit(1).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}
func existingCommandOutcome(p *AgentProposal, i commandIntent) (*CommandProposalOutcome, error) {
	var f commandForm
	if err := json.Unmarshal(p.Payload, &f); err != nil {
		return nil, err
	}
	if f.V != 1 {
		return nil, errors.New("unsupported stored command proposal version")
	}
	a, err := actiongate.Canonical(f.Intent)
	if err != nil {
		return nil, err
	}
	b, err := actiongate.Canonical(i)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(a, b) {
		return nil, conflict.New("this requestId was already used for a different proposal")
	}
	out := &CommandProposalOutcome{Status: ProposalStatusExisting, Proposal: p, Digest: p.Digest, TargetCount: p.TargetCount, RequiredApprovals: p.RequiredApprovals, Verdict: actiongate.VerdictApprove, Preview: f.preview(nil)}
	// APPROVE means "pending people's approval", which only a PROPOSED row still is. A decided
	// row (refused, rejected, expired, killed, executed) is not awaiting approval, so it is
	// never answered APPROVE; proposal.state says how it ended.
	if p.State != string(actiongate.StateProposed) {
		out.Verdict = actiongate.VerdictRefuse
		out.RequiredApprovals = 0
	}
	if err := json.Unmarshal(p.Reasons, &out.Reasons); err != nil {
		return nil, err
	}
	out.Reasons = actiongate.PersistedReasons(out.Reasons)
	if p.State == string(actiongate.StateProposed) {
		exp := p.ExpiresAt
		out.ExpiresAt = &exp
	}
	return out, nil
}
func requireProposalTenant(ctx context.Context) (string, error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return "", errors.New("agent execution requires tenant context")
	}
	return tenant, nil
}
