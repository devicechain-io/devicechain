// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sort"
	"syscall"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	lockTimeout      = "3s"
	statementTimeout = "8s"
)

func setAgentExecutionTimeouts(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec("SET LOCAL lock_timeout = '" + lockTimeout + "'").Error; err != nil {
		return err
	}
	return tx.Exec("SET LOCAL statement_timeout = '" + statementTimeout + "'").Error
}

func agentExecutionLockError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "55P03" {
		return errConcurrentApproval
	}
	return err
}

// connectionLost recognizes transport loss, including pgx's closed-connection sentinel.
func connectionLost(err error) bool {
	for _, cause := range []error{driver.ErrBadConn, io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, syscall.ECONNRESET, syscall.EPIPE, pgconn.ErrConnClosed} {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}

var errRefusedAtCommitPoint = errors.New("agent proposal refused at commit point")

// executeAlarmProposal consumes the final approval and its effects atomically.
func (api *Api) executeAlarmProposal(ctx context.Context, approver actiongate.Approver, p *AgentProposal, next []actiongate.Approval, preRequired int) (result *AgentProposal, resultErr error) {
	started := time.Now()
	// committed is the outcome once it is durable, so a failed read-back after COMMIT is not
	// counted as an execution that could not complete.
	var committed string
	defer func() {
		res := committed
		if res == "" {
			res = executionResult(result, resultErr)
		}
		api.AgentMetrics.execution(actiongate.Kind(p.Kind), res, time.Since(started))
	}()
	return api.executeAlarmProposalAttempt(ctx, approver, p, next, preRequired, false, &committed)
}

func (api *Api) executeAlarmProposalAttempt(ctx context.Context, approver actiongate.Approver, p *AgentProposal, next []actiongate.Approval, preRequired int, retried bool, committed *string) (*AgentProposal, error) {
	attrCtx := rdb.WithAgentAttribution(ctx, rdb.AgentAttribution{ClientID: p.ProposerClient, OnBehalfOf: p.ProposerSub, Proposal: p.Token})
	reenableGeneration := 0
	dependencyRefused := false
	var reasons []string
	decisionAsked := false
	err := api.RDB.DB(attrCtx).Transaction(func(tx *gorm.DB) error {
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
		p = &locked
		// Take all locks first and run every statement before the commit-point decision.
		// The tail rule requires no SQL after that decision, only in-memory checks until COMMIT.
		// ReadFenceIfEnforced explicitly reads the erasure fence before effects: their fence
		// read is memoised for this transaction, so this makes the order explicit and fails
		// closed before any effect is written. A commit-point refusal rolls everything back
		// and is recorded in a separate transaction, keeping the tail SQL-free and reading
		// the erasure fence afresh. A refusal written here would reuse the earlier clear memo.
		var targets []string
		var payload struct {
			Action AlarmProposalAction `json:"action"`
		}
		var dependencies map[string]map[string]string
		if err := json.Unmarshal(p.TargetSet, &targets); err != nil {
			return err
		}
		if len(targets) == 0 {
			return errors.New("alarm proposal has no stored targets")
		}
		seen := map[string]bool{}
		for _, token := range targets {
			if token == "" || seen[token] {
				return errors.New("invalid stored alarm target set")
			}
			seen[token] = true
		}
		if err := json.Unmarshal(p.Payload, &payload); err != nil {
			return err
		}
		if payload.Action.kind() == "" || string(payload.Action.kind()) != p.Kind {
			return fmt.Errorf("unimplemented alarm proposal action %q", payload.Action)
		}
		if err := json.Unmarshal(p.Dependencies, &dependencies); err != nil {
			return err
		}
		if dependencies == nil {
			return errors.New("missing stored alarm dependencies")
		}
		raised, ok := dependencies[proposalDependencyRaisedTimes]
		if !ok || raised == nil {
			return errors.New("missing stored raisedTimes")
		}
		byToken := map[string]*Alarm{}
		// Sort a copy so every execution locks in global token order, across chunks too.
		lockTargets := slices.Clone(targets)
		sort.Strings(lockTargets)
		for start := 0; start < len(lockTargets); start += rdb.MaxLookupKeys {
			var alarms []*Alarm
			end := min(start+rdb.MaxLookupKeys, len(lockTargets))
			if err := rdb.FindByKeys(tx.Clauses(clause.Locking{Strength: "UPDATE"}).Order("token"), &alarms, "token", lockTargets[start:end]); err != nil {
				return agentExecutionLockError(err)
			}
			for _, alarm := range alarms {
				byToken[alarm.Token] = alarm
			}
		}
		refuse := func(reasons []string) error {
			raw, err := json.Marshal(actiongate.PersistedReasons(reasons))
			if err != nil {
				return err
			}
			return actiongate.Transition(tx, &AgentProposal{}, p.Token, actiongate.StateProposed, actiongate.StateRefused, now,
				map[string]any{"decided_at": now, "decided_by": approver.Principal, "reasons": datatypes.JSON(raw)})
		}

		changed := len(raised) != len(targets) || len(byToken) != len(targets)
		for _, token := range targets {
			alarm := byToken[token]
			if alarm == nil || raised[token] != alarm.RaisedTime.UTC().Format(time.RFC3339Nano) {
				changed = true
			}
		}
		if changed {
			dependencyRefused = true
			return refuse([]string{actiongate.ReasonDependencyChanged})
		}
		if api.beforeFenceForTest != nil {
			if err := api.beforeFenceForTest(tx); err != nil {
				return err
			}
		}
		tenant, ok := core.TenantFromContext(ctx)
		if !ok || tenant == "" {
			return errors.New("agent execution requires tenant context")
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
		for _, token := range targets {
			var ev *AlarmStateChangeEvent
			switch payload.Action {
			case AlarmProposalAck:
				ev, err = ackAlarmTx(ctx, tx, byToken[token], &approver.Principal)
			case AlarmProposalClear:
				ev, err = clearAlarmTx(ctx, tx, byToken[token])
			}
			if err != nil {
				return err
			}
			if ev != nil {
				if err := queueAgentEffect(tx, p.Token, ev); err != nil {
					return err
				}
			}
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		now = time.Now().UTC()
		if err := actiongate.Transition(tx, &AgentProposal{}, p.Token, actiongate.StateProposed, actiongate.StateExecuted, now, map[string]any{
			"decided_at": now, "decided_by": approver.Principal, "executed_at": now, "result_ref": p.EffectToken,
			"approvals": datatypes.JSON(raw), "revision": p.Revision + 1, "required_approvals": preRequired,
		}); err != nil {
			return err
		}
		sid := ""
		if p.ProposerSid != nil {
			sid = *p.ProposerSid
		}
		fresh, err := actiongate.CommitPoint(ctx, tx, func(dctx context.Context) (actiongate.ExecutionDecision, error) {
			return api.AgentDecider.Execution(dctx, actiongate.ExecutionRequest{
				ProposalToken: p.Token, Kind: actiongate.Kind(p.Kind), Impact: p.Impact, TargetCount: p.TargetCount,
				ProposerPrincipal: p.ProposerSub, ProposerClient: p.ProposerClient, ProposerSid: sid, Approvers: actiongate.Principals(next), ApproverSessions: actiongate.ApproverSessions(next),
			})
		})
		decisionAsked = !errors.Is(err, actiongate.ErrCommitPointSetup)
		if api.afterDecisionForTest != nil {
			if hookErr := api.afterDecisionForTest(tx); hookErr != nil {
				return hookErr
			}
		}
		// SQL-free tail: no transaction operations may follow the commit-point decision.
		if err != nil {
			if errors.Is(err, actiongate.ErrCommitPointSetup) || errors.Is(err, auth.ErrForbidden) {
				return err
			}
			return actiongate.ErrPolicyUnavailable
		}
		if fresh.Verdict != actiongate.VerdictApprove {
			reasons = actiongate.PersistedReasons(fresh.Reasons)
			if fresh.Verdict == actiongate.VerdictAuto {
				reasons = append(reasons, actiongate.ReasonAutoNotAvailable)
			}
			return errRefusedAtCommitPoint
		}
		if !slices.Contains(fresh.ValidApprovers, approver.Principal) {
			return errNotAnApprover
		}
		if err := actiongate.CheckStepUp(approver, fresh.StepUp, time.Now().UTC()); err != nil {
			return err
		}
		if approver.Principal == p.ProposerSub && (!fresh.AllowSelfConfirm || fresh.RequiredApprovals > 1) {
			return errSelfApproval
		}
		if fresh.RequiredApprovals < 1 || fresh.RequiredApprovals != preRequired {
			return errConcurrentApproval
		}
		now2 := time.Now().UTC()
		if actiongate.Tally(next, fresh.ValidApprovers, p.ProposerSub, fresh.RequiredApprovals, fresh.AllowSelfConfirm, fresh.StepUp, now2) < fresh.RequiredApprovals {
			return errConcurrentApproval
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
	// Postgres terminated the idle transaction before the decision could commit.
	var pgerr *pgconn.PgError
	if (errors.As(err, &pgerr) && pgerr.Code == "25P03") || (decisionAsked && connectionLost(err)) {
		return nil, actiongate.ErrPolicyUnavailable
	}
	if errors.Is(err, errFenceNeedsReenable) {
		if retried {
			return nil, actiongate.ErrFenceClosed
		}
		if err := api.reenableFence(ctx, reenableGeneration); err != nil {
			return nil, err
		}
		return api.executeAlarmProposalAttempt(ctx, approver, p, next, preRequired, true, committed)
	}
	if errors.Is(err, errRefusedAtCommitPoint) {
		ended, err := api.endProposal(attrCtx, p.Token, actiongate.StateRefused, approver.Principal, nil, reasons, time.Now().UTC())
		if err == nil {
			*committed = "policy_refused"
		}
		return ended, err
	}
	if err != nil {
		return nil, err
	}
	if dependencyRefused {
		*committed = "dependency_changed"
	} else {
		*committed = "executed"
	}
	return api.AgentProposalByToken(ctx, p.Token, nil)
}
