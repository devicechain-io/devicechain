// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func setAgentExecutionTimeouts(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec("SET LOCAL lock_timeout = '3s'").Error; err != nil {
		return err
	}
	return tx.Exec("SET LOCAL statement_timeout = '8s'").Error
}
func agentExecutionLockError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return errConcurrentApproval
	}
	return err
}
func connectionLost(err error) bool {
	for _, cause := range []error{driver.ErrBadConn, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.EPIPE, pgconn.ErrConnClosed} {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}
func proposalUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return true
	}
	return errors.Is(err, gorm.ErrDuplicatedKey) || conflict.Is(err) || (err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed"))
}

var errRefusedAtCommitPoint = errors.New("agent proposal refused at commit point")

// errProposalAlreadyAdmitted refuses a second admission of one proposal. It is not retryable.
var errProposalAlreadyAdmitted = actiongate.Refusal("ALREADY_ADMITTED", "this proposal's command was already admitted; it is never admitted twice", false)

func (api *Api) executeCommandProposal(ctx context.Context, a actiongate.Approver, p *AgentProposal, next []actiongate.Approval, required int, _ bool) (*AgentProposal, error) {
	return api.executeCommandProposalAttempt(ctx, a, p, next, required, false)
}
func (api *Api) executeCommandProposalAttempt(ctx context.Context, a actiongate.Approver, p *AgentProposal, next []actiongate.Approval, required int, retried bool) (*AgentProposal, error) {
	f, err := verifyCommandProposal(p)
	if err != nil {
		return nil, err
	}
	cutoff, _ := time.Parse(time.RFC3339, f.Effective.Cutoff)
	if !api.agentNow().Before(cutoff) {
		return api.refuseCommandCutoff(ctx, p, a.Principal)
	}
	// Ruling 3: this fresh caller-token read checks the frozen snapshot. The
	// remote read is NOT a lock: a remote republish can still overtake COMMIT.
	target, err := api.readProposalTarget(ctx, f.Intent, cutoff)
	if err != nil {
		return nil, err
	}
	incarnation, _ := strconv.ParseUint(f.Effective.Incarnation, 10, 63)
	if !target.Exists || target.Incarnation != incarnation || !target.Constrained || !target.Found || target.DefinitionFingerprint != f.Effective.Definition || target.Impact != f.Effective.Impact || !target.Allowed {
		return api.endProposal(ctx, p.Token, actiongate.StateRefused, a.Principal, nil, []string{actiongate.ReasonDependencyChanged}, time.Now().UTC())
	}
	if target.ScheduleRefusal != nil {
		return api.endProposal(ctx, p.Token, actiongate.StateRefused, a.Principal, nil, []string{targetRefusal(target)}, time.Now().UTC())
	}
	attrCtx := rdb.WithAgentAttribution(ctx, rdb.AgentAttribution{ClientID: p.ProposerClient, OnBehalfOf: p.ProposerSub, Proposal: p.Token})
	tenant, err := requireProposalTenant(ctx)
	if err != nil {
		return nil, err
	}
	var reasons []string
	reenableGeneration := 0
	decisionAsked := false
	var created *Command
	err = api.RDB.DB(attrCtx).Transaction(func(tx *gorm.DB) error {
		if err := setAgentExecutionTimeouts(tx); err != nil {
			return err
		}
		var locked AgentProposal
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("token = ?", p.Token).First(&locked).Error; err != nil {
			return agentExecutionLockError(err)
		}
		if locked.State != string(actiongate.StateProposed) || locked.Revision != p.Revision {
			return actiongate.ErrNotPending
		}
		now := time.Now().UTC()
		if !locked.ExpiresAt.After(now) {
			return errProposalExpired
		}
		lockedForm, err := verifyCommandProposal(&locked)
		if err != nil {
			return err
		}
		if !actiongate.DigestEqual(locked.Digest, p.Digest) {
			return ErrProposalDigestMismatch
		}
		p = &locked
		f = lockedForm
		if err := lockCommandCounters(tx, actiongate.Proposer{Tenant: tenant, Client: p.ProposerClient, Principal: p.ProposerSub}); err != nil {
			return err
		}
		if err := checkCommandExecutionBudgets(tx, p, api.agentNow()); err != nil {
			return err
		}
		if api.beforeFenceForTest != nil {
			if err := api.beforeFenceForTest(tx); err != nil {
				return err
			}
		}
		if err := rdb.ReadFenceIfEnforced(attrCtx, tx, tenant); err != nil {
			return err
		}
		if api.KillFeed == nil || !api.KillFeed.Fresh(api.KillFeed.Now()) {
			return actiongate.ErrFenceUnavailable
		}
		state, err := fenceReadTx(tx)
		if err != nil {
			return err
		}
		if api.afterFenceForTest != nil {
			if err := api.afterFenceForTest(tx); err != nil {
				return err
			}
		}
		created = &Command{TokenReference: rdb.TokenReference{Token: p.EffectToken}, DeviceToken: f.Intent.DeviceToken, Name: f.Intent.Command, DeviceIncarnation: nullableIncarnation(incarnation), ScopeAnchors: rdb.Anchors(target.Anchors), Status: CommandQueued.String(), QueuedTime: api.agentNow(), ExpiresAt: sql.NullTime{Time: cutoff, Valid: true}, CauseKind: sql.NullString{String: CauseKindProposal, Valid: true}, ProposalToken: sql.NullString{String: p.Token, Valid: true}}
		if f.Intent.PayloadPresent {
			payload := datatypes.JSON(append([]byte(nil), f.Intent.Payload...))
			created.Payload = &payload
		}
		if target.Hold != nil {
			stampHold(created, *target.Hold)
		}
		if err := api.insertProposalCommandTx(attrCtx, tx, created); err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		now = time.Now().UTC()
		if err := actiongate.Transition(tx, &AgentProposal{}, p.Token, actiongate.StateProposed, actiongate.StateExecuted, now, map[string]any{"decided_at": now, "decided_by": a.Principal, "executed_at": now, "result_ref": p.EffectToken, "approvals": datatypes.JSON(raw), "revision": p.Revision + 1, "required_approvals": required}); err != nil {
			return err
		}
		sid := ""
		if p.ProposerSid != nil {
			sid = *p.ProposerSid
		}
		fresh, err := actiongate.CommitPoint(ctx, tx, func(dctx context.Context) (actiongate.ExecutionDecision, error) {
			return api.AgentDecider.Execution(dctx, actiongate.ExecutionRequest{Command: f.Intent.Command, ProposalToken: p.Token, Kind: actiongate.KindCommandSend, Impact: p.Impact, TargetCount: 1, ProposerPrincipal: p.ProposerSub, ProposerClient: p.ProposerClient, ProposerSid: sid, Approvers: actiongate.Principals(next), ApproverSessions: actiongate.ApproverSessions(next)})
		})
		decisionAsked = !errors.Is(err, actiongate.ErrCommitPointSetup)
		if api.afterDecisionForTest != nil {
			if hookErr := api.afterDecisionForTest(tx); hookErr != nil {
				return hookErr
			}
		}
		// SQL-free tail. Refusals roll back and are recorded in a separate
		// transaction with a fresh erasure-fence read, exactly as alarms do.
		if err != nil {
			if errors.Is(err, actiongate.ErrCommitPointSetup) || errors.Is(err, auth.ErrForbidden) {
				return err
			}
			return actiongate.ErrPolicyUnavailable
		}
		reason := commandDecisionRefusal(fresh.Verdict, fresh.GroupRestriction, p.Impact, fresh.RequiredApprovals)
		if fresh.Verdict != actiongate.VerdictApprove || reason != "" {
			reasons = actiongate.PersistedReasons(fresh.Reasons)
			if reason != "" {
				reasons = append(reasons, reason)
			}
			return errRefusedAtCommitPoint
		}
		if !slices.Contains(fresh.ValidApprovers, a.Principal) {
			return errNotAnApprover
		}
		now2 := time.Now().UTC()
		if err := actiongate.CheckStepUp(a, fresh.StepUp, now2); err != nil {
			return err
		}
		if a.Principal == p.ProposerSub && (!fresh.AllowSelfConfirm || fresh.RequiredApprovals > 1 || p.Impact != "NORMAL") {
			return errSelfApproval
		}
		if fresh.RequiredApprovals < 1 || fresh.RequiredApprovals != required {
			return errConcurrentApproval
		}
		if actiongate.Tally(next, fresh.ValidApprovers, p.ProposerSub, fresh.RequiredApprovals, fresh.AllowSelfConfirm, fresh.StepUp, now2) < fresh.RequiredApprovals {
			return errConcurrentApproval
		}

		if !api.agentNow().Before(cutoff) {
			reasons = []string{"COMMAND_CUTOFF_PASSED"}
			return errRefusedAtCommitPoint
		}
		if !p.ExpiresAt.After(now2) {
			return errProposalExpired
		}
		if api.KillFeed == nil || !api.KillFeed.Fresh(api.KillFeed.Now()) {
			return actiongate.ErrFenceUnavailable
		}
		if err := fenceVerdictErr(state, fresh.ActuationGeneration); err != nil {
			if errors.Is(err, errFenceNeedsReenable) {
				reenableGeneration = fresh.ActuationGeneration
			}
			return err
		}
		return nil
	})
	var pg *pgconn.PgError
	if (errors.As(err, &pg) && pg.Code == "25P03") || (decisionAsked && connectionLost(err)) {
		return nil, actiongate.ErrPolicyUnavailable
	}
	if errors.Is(err, errFenceNeedsReenable) {
		if retried {
			return nil, actiongate.ErrFenceClosed
		}
		if err := api.reenableFence(ctx, reenableGeneration); err != nil {
			return nil, err
		}
		return api.executeCommandProposalAttempt(ctx, a, p, next, required, true)
	}
	if errors.Is(err, errRefusedAtCommitPoint) {
		if slices.Contains(reasons, "COMMAND_CUTOFF_PASSED") {
			return api.refuseCommandCutoff(attrCtx, p, a.Principal)
		}
		return api.endProposal(attrCtx, p.Token, actiongate.StateRefused, a.Principal, nil, reasons, time.Now().UTC())
	}
	if err != nil {
		return nil, err
	}
	// Only the successful OUTER COMMIT permits a dispatch hint. Never nudge on
	// rollback, token collision, refusal, or a lost/ambiguous COMMIT response.
	api.nudgeDispatch(ctx, created)
	return api.AgentProposalByToken(ctx, p.Token, nil)
}

// insertProposalCommandTx deliberately uses a PLAIN Create. Direct enqueue's
// ON CONFLICT replay/adoption semantics would consume an unrelated effect token.
//
// A command already carrying this proposal's token, SOFT-DELETED ones included (gorm's
// default scope would hide those and let the insert run into the unique
// (tenant_id, proposal_token) index), means the proposal was already admitted: that is a
// terminal refusal, never a retryable error and never a second command. The caller holds
// the proposal's row lock, and only this path writes proposal_token, so the probe and the
// insert cannot be raced by another admission of the same proposal.
func (api *Api) insertProposalCommandTx(ctx context.Context, tx *gorm.DB, created *Command) error {
	if created.ProposalToken.Valid {
		var admitted int64
		if err := tx.Unscoped().Model(&Command{}).Where("proposal_token = ?", created.ProposalToken.String).Count(&admitted).Error; err != nil {
			return err
		}
		if admitted > 0 {
			return errProposalAlreadyAdmitted
		}
	}
	if err := api.checkUndeliveredCeiling(ctx, tx); err != nil {
		return err
	}
	err := tx.Create(created).Error
	if proposalUniqueViolation(err) {
		return actiongate.Refusal("TOKEN_IN_USE", "command token or proposal effect is already in use", false)
	}
	return err
}
func retryableCommandRefusal(code string) *actiongate.Error {
	e := actiongate.Refusal(code, "agent command execution budget is temporarily exhausted", false)
	e.Retryable = true
	return e
}
func checkCommandExecutionBudgets(tx *gorm.DB, p *AgentProposal, now time.Time) error {
	// ExecutionDecision has no actionsPerHour. Use only platform constants;
	// never carry a stale proposal-time policy allowance into execution.
	since := now.Add(-time.Hour)
	for _, limit := range []struct {
		column, value string
		cap           int
	}{{"proposer_client", p.ProposerClient, ExecutionsPerHourPerClient}, {"proposer_sub", p.ProposerSub, ExecutionsPerHourPerPrincipal}} {
		var count int64
		if err := tx.Model(&AgentProposal{}).Where("state = ? AND executed_at > ?", string(actiongate.StateExecuted), since).Where(limit.column+" = ?", limit.value).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(limit.cap) {
			return retryableCommandRefusal("EXECUTION_RATE_EXCEEDED")
		}
	}
	var count int64
	if err := tx.Model(&AgentProposal{}).Where("state = ? AND executed_at > ?", string(actiongate.StateExecuted), since).Count(&count).Error; err != nil {
		return err
	}
	if count >= TenantExecutionsPerHour {
		return retryableCommandRefusal("TENANT_EXECUTION_RATE_EXCEEDED")
	}
	return nil
}
