// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
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

// executeAlarmProposal consumes the final approval and its effects atomically.
func (api *Api) executeAlarmProposal(ctx context.Context, approver actiongate.Approver, p *AgentProposal, next []actiongate.Approval) (*AgentProposal, error) {
	attrCtx := rdb.WithAgentAttribution(ctx, rdb.AgentAttribution{ClientID: p.ProposerClient, OnBehalfOf: p.ProposerSub, Proposal: p.Token})
	var events []*AlarmStateChangeEvent
	executed := false
	started := time.Now()
	metricResult := "policy_refused"
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
		// Parse the stored action and LOCK every target alarm BEFORE asking the policy: a wait for an
		// alarm row must not sit between the decision and the effect, or a switch thrown during the
		// wait would be answered by a decision that is already stale.
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
		sid := ""
		if p.ProposerSid != nil {
			sid = *p.ProposerSid
		}
		fresh, err := api.AgentDecider.Execution(ctx, actiongate.ExecutionRequest{
			ProposalToken: p.Token, Kind: actiongate.Kind(p.Kind), Impact: p.Impact, TargetCount: p.TargetCount,
			ProposerPrincipal: p.ProposerSub, ProposerClient: p.ProposerClient, ProposerSid: sid, Approvers: actiongate.Principals(next),
		})
		if err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return err
			}
			return actiongate.ErrPolicyUnavailable
		}
		now = time.Now().UTC()
		refuse := func(reasons []string) error {
			raw, err := json.Marshal(actiongate.PersistedReasons(reasons))
			if err != nil {
				return err
			}
			return actiongate.Transition(tx, &AgentProposal{}, p.Token, actiongate.StateProposed, actiongate.StateRefused, now,
				map[string]any{"decided_at": now, "decided_by": approver.Principal, "reasons": datatypes.JSON(raw)})
		}
		if fresh.Verdict != actiongate.VerdictApprove {
			reasons := actiongate.PersistedReasons(fresh.Reasons)
			if fresh.Verdict == actiongate.VerdictAuto {
				reasons = append(reasons, actiongate.ReasonAutoNotAvailable)
			}
			return refuse(reasons)
		}
		if !slices.Contains(fresh.ValidApprovers, approver.Principal) {
			return errNotAnApprover
		}
		if err := actiongate.CheckStepUp(approver, fresh.StepUp, now); err != nil {
			return err
		}
		if approver.Principal == p.ProposerSub && (!fresh.AllowSelfConfirm || fresh.RequiredApprovals > 1) {
			return errSelfApproval
		}
		if fresh.RequiredApprovals < 1 || actiongate.Tally(next, fresh.ValidApprovers, p.ProposerSub, fresh.RequiredApprovals, fresh.AllowSelfConfirm, fresh.StepUp, now) < fresh.RequiredApprovals {
			return errConcurrentApproval
		}

		changed := len(raised) != len(targets) || len(byToken) != len(targets)
		for _, token := range targets {
			alarm := byToken[token]
			if alarm == nil || raised[token] != alarm.RaisedTime.UTC().Format(time.RFC3339Nano) {
				changed = true
			}
		}
		if changed {
			metricResult = "dependency_changed"
			return refuse([]string{actiongate.ReasonDependencyChanged})
		}
		// The clock again: the checks above are only as fresh as the moment they ran.
		now = time.Now().UTC()
		if !p.ExpiresAt.After(now) {
			return errProposalExpired
		}
		if actiongate.Tally(next, fresh.ValidApprovers, p.ProposerSub, fresh.RequiredApprovals, fresh.AllowSelfConfirm, fresh.StepUp, now) < fresh.RequiredApprovals {
			return errConcurrentApproval
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
				events = append(events, ev)
			}
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := actiongate.Transition(tx, &AgentProposal{}, p.Token, actiongate.StateProposed, actiongate.StateExecuted, now, map[string]any{
			"decided_at": now, "decided_by": approver.Principal, "executed_at": now, "result_ref": p.EffectToken,
			"approvals": datatypes.JSON(raw), "revision": p.Revision + 1, "required_approvals": fresh.RequiredApprovals,
		}); err != nil {
			return err
		}
		executed = true
		return nil
	})
	if err != nil {
		metricResult = "failed"
	} else if executed {
		metricResult = "executed"
	}
	api.AgentMetrics.execution(actiongate.Kind(p.Kind), metricResult, time.Since(started))
	if err != nil {
		return nil, err
	}
	if executed {
		for _, ev := range events {
			api.emitAlarmEvent(ctx, ev)
		}
	}
	return api.AgentProposalByToken(ctx, p.Token, nil)
}
