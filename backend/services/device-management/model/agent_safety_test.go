// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func killFor(tenant string, generation int) actiongate.KillEvent {
	return actiongate.KillEvent{EventID: "kill", Tenant: tenant, Cause: actiongate.KillCauseTenantDisabled, ActuationGeneration: generation, At: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
}

func effectRows(t *testing.T, api *Api, ctx context.Context) []AgentEffectOutbox {
	t.Helper()
	var rows []AgentEffectOutbox
	require.NoError(t, api.RDB.DB(ctx).Order("created_at").Order("id").Find(&rows).Error)
	return rows
}

func requireUnexecuted(t *testing.T, r *proposalRig, p *AgentProposal, ctx context.Context) {
	t.Helper()
	require.Equal(t, string(actiongate.StateProposed), r.stored(t, p.Token).State)
	alarms, err := r.api.AlarmsByToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.False(t, alarms[0].Acknowledged)
	require.Empty(t, effectRows(t, r.api, ctx))
}

func TestAgentFenceFeedFailsClosed(t *testing.T) {
	for _, mode := range []string{"nil", "unbeaten", "stale"} {
		t.Run(mode, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			r.api.KillFeed = NewKillFeed(time.Second, func() time.Time { return now })
			switch mode {
			case "nil":
				r.api.KillFeed = nil
			case "stale":
				r.api.KillFeed.Beat(now)
				now = now.Add(2 * time.Second)
			}
			_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			require.ErrorIs(t, err, actiongate.ErrFenceUnavailable)
			requireUnexecuted(t, r, p, ctx)
		})
	}
}

func TestAgentFenceKillDuringDecisionRollsBack(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	// SQLite cannot commit a second writer while its in-memory execution transaction
	// is open. Apply through that transaction here; the Postgres test commits a real
	// concurrent kill on a separate connection.
	r.api.afterDecisionForTest = func(tx *gorm.DB) error {
		return applyKillEventDB(tx, rigTenant, killFor(rigTenant, r.decider.exec.ActuationGeneration))
	}
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, actiongate.ErrFenceClosed)
	requireUnexecuted(t, r, p, ctx)
}

func TestAgentFenceReenableRequiresNewerFreshDecision(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
	policyVersion := r.decider.exec.PolicyVersion
	v := r.decider.exec.ActuationGeneration + 1
	r.decider.exec.ActuationGeneration = v
	require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, v)))
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.ErrorIs(t, err, actiongate.ErrFenceClosed)
	requireUnexecuted(t, r, p, ctx)
	r.decider.exec.ActuationGeneration = v + 1
	before := len(r.decider.execReqs)
	got, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), got.State)
	require.Equal(t, policyVersion, r.decider.exec.PolicyVersion, "restoring actuation does not publish a policy")
	require.Equal(t, 3, len(r.decider.execReqs)-before, "approval, execution, then one fresh execution retry")
	require.Len(t, effectRows(t, r.api, ctx), 1)
	var fence AgentActuationFence
	require.NoError(t, r.api.RDB.DB(ctx).First(&fence).Error)
	require.True(t, fence.Enabled)
	require.EqualValues(t, 2, fence.Epoch)
	_, err = r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.Error(t, err)
	require.Len(t, effectRows(t, r.api, ctx), 1, "the restored proposal executes only once")
	require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, v+1)))
	p2 := r.propose(t, rigAgent, clearReq("after-rekill", "a1")).Proposal
	_, err = r.api.ApproveAgentProposal(ctx, person, p2.Token, p2.Digest)
	require.ErrorIs(t, err, actiongate.ErrFenceClosed)
	require.Equal(t, string(actiongate.StateProposed), r.stored(t, p2.Token).State)
	require.Len(t, effectRows(t, r.api, ctx), 1)
}

func TestApplyKillEventMonotonicAndTenantScoped(t *testing.T) {
	r := newProposalRig(t)
	ctx := tenantCtx(rigTenant)
	for i, v := range []int{7, 7, 3, 9} {
		require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, v)))
		var row AgentActuationFence
		require.NoError(t, r.api.RDB.DB(ctx).First(&row).Error)
		require.False(t, row.Enabled)
		require.EqualValues(t, i+1, row.Epoch)
		require.Equal(t, max(7, v), row.KilledAtGeneration)
	}
	require.NoError(t, r.api.reenableFence(ctx, 10))
	require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, 1)))
	var row AgentActuationFence
	require.NoError(t, r.api.RDB.DB(ctx).First(&row).Error)
	require.False(t, row.Enabled)
	require.Equal(t, 9, row.KilledAtGeneration)
	require.EqualValues(t, 6, row.Epoch)
	require.Error(t, r.api.ApplyKillEvent(tenantCtx("other"), killFor(rigTenant, 50)))
	require.Error(t, r.api.ApplyKillEvent(context.Background(), killFor(rigTenant, 50)))
	var count int64
	require.NoError(t, r.api.RDB.DB(tenantCtx("other")).Model(&AgentActuationFence{}).Count(&count).Error)
	require.Zero(t, count)
}

type dedupEffectPublisher struct {
	seen    map[string]string
	ids     []string
	fail    map[string]bool
	tenants []string
}

func (p *dedupEffectPublisher) PublishAlarmEvent(context.Context, *AlarmStateChangeEvent) {
	panic("outbox used non-dedup publish")
}
func (p *dedupEffectPublisher) PublishAlarmEventDedup(ctx context.Context, e *AlarmStateChangeEvent, id string) error {
	p.ids = append(p.ids, id)
	if p.fail[e.AlarmToken] {
		return errors.New("broker refused")
	}
	if p.seen == nil {
		p.seen = map[string]string{}
	}
	p.seen[id] = e.AlarmToken
	tenant, _ := core.TenantFromContext(ctx)
	p.tenants = append(p.tenants, tenant)
	return nil
}

func TestAgentEffectsSurviveRestart(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	pub := &dedupEffectPublisher{}
	r.api.AlarmPublisher = pub
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	require.Empty(t, pub.ids)
	rows := effectRows(t, r.api, ctx)
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.Len(t, row.Id, 32)
		require.Equal(t, p.Token, row.ProposalToken)
	}
	restarted := NewApi(r.api.RDB)
	restarted.AlarmPublisher = pub
	res, err := restarted.DrainAgentEffects(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 2, res.Published)
	require.Len(t, pub.seen, 2)
	require.Equal(t, []string{rigTenant, rigTenant}, pub.tenants)
	require.Empty(t, effectRows(t, restarted, ctx))
	_, err = restarted.DrainAgentEffects(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pub.ids, 2)
}

func TestAgentEffectsRetryPublishAndLostDeleteAck(t *testing.T) {
	for _, mode := range []string{"publish", "delete"} {
		t.Run(mode, func(t *testing.T) {
			r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1")
			_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
			require.NoError(t, err)
			pub := &dedupEffectPublisher{fail: map[string]bool{"a1": mode == "publish"}}
			r.api.AlarmPublisher = pub
			if mode == "delete" {
				require.NoError(t, r.db.Callback().Delete().Before("gorm:delete").Register("lost_delete", func(db *gorm.DB) {
					if db.Statement.Table == "agent_effect_outboxes" {
						db.AddError(errors.New("delete failed"))
					}
				}))
				t.Cleanup(func() { r.db.Callback().Delete().Remove("lost_delete") })
			}
			_, err = r.api.DrainAgentEffects(ctx, 10)
			rows := effectRows(t, r.api, ctx)
			require.Len(t, rows, 1)
			if mode == "publish" {
				require.Equal(t, 1, rows[0].Attempts)
				require.NotNil(t, rows[0].LastError)
			} else {
				require.Error(t, err)
				require.Len(t, pub.seen, 1)
				require.NoError(t, r.db.Callback().Delete().Remove("lost_delete"))
			}
			pub.fail["a1"] = false
			_, err = r.api.DrainAgentEffects(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, []string{rows[0].Id, rows[0].Id}, pub.ids)
			require.Len(t, pub.seen, 1, "the broker's dedup window suppresses a repeated ID")
			require.Empty(t, effectRows(t, r.api, ctx))
		})
	}
}

func TestAgentEffectsPoisonDoesNotBlockLaterPage(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	rows := effectRows(t, r.api, ctx)
	// Make the failing row deterministically oldest.
	for _, row := range rows {
		if string(row.Payload) != "" {
			// Queue insertion follows target order, but identify it from its payload.
			var event AlarmStateChangeEvent
			require.NoError(t, json.Unmarshal(row.Payload, &event))
			// Fixed, whole-second times: SQLite compares timestamps as text, so a fractional-second
			// clock reading could order a rotated row ahead of an untouched one.
			year := 2021
			if event.AlarmToken == "a1" {
				year = 2020
			}
			require.NoError(t, r.api.RDB.DB(ctx).Model(&AgentEffectOutbox{}).Where("id = ?", row.Id).Update("created_at", time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)).Error)
		}
	}
	pub := &dedupEffectPublisher{fail: map[string]bool{"a1": true}}
	r.api.AlarmPublisher = pub
	_, _ = r.api.DrainAgentEffects(ctx, 1)
	_, err = r.api.DrainAgentEffects(ctx, 1)
	require.NoError(t, err)
	require.Len(t, pub.seen, 1)
	for _, token := range pub.seen {
		require.Equal(t, "a2", token)
	}
	require.Len(t, effectRows(t, r.api, ctx), 1)
}

func TestAgentEffectsStopAtAttemptCapWithoutBlockingOtherRows(t *testing.T) {
	r, p, ctx, person := executionSetup(t, AlarmProposalAck, "a1", "a2")
	_, err := r.api.ApproveAgentProposal(ctx, person, p.Token, p.Digest)
	require.NoError(t, err)
	pub := &dedupEffectPublisher{fail: map[string]bool{"a1": true, "a2": true}}
	r.api.AlarmPublisher = pub
	r.api.AgentEffectsExhausted = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_agent_effects_exhausted_total"})
	for i := 0; i < MaxAgentEffectAttempts; i++ {
		_, err = r.api.DrainAgentEffects(ctx, 10)
		require.Error(t, err)
		if i == 0 {
			pub.fail["a2"] = false
		}
	}
	require.Len(t, pub.ids, MaxAgentEffectAttempts+2, "a2 retries once while a1 exhausts the cap")
	require.Len(t, pub.seen, 1)
	rows := effectRows(t, r.api, ctx)
	require.Len(t, rows, 1)
	require.Equal(t, MaxAgentEffectAttempts, rows[0].Attempts)
	require.NotNil(t, rows[0].LastError)
	require.Equal(t, 1.0, testutil.ToFloat64(r.api.AgentEffectsExhausted))
	for i := 0; i < 3; i++ {
		res, err := r.api.DrainAgentEffects(ctx, 10)
		require.NoError(t, err)
		require.Zero(t, res.Published)
	}
	require.Len(t, pub.ids, MaxAgentEffectAttempts+2)
	require.Equal(t, 1.0, testutil.ToFloat64(r.api.AgentEffectsExhausted), "exhaustion is counted only once")
	// New work continues even with a retained exhausted row.
	require.NoError(t, r.api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		return queueAgentEffect(tx, p.Token, &AlarmStateChangeEvent{AlarmToken: "a2", EventType: AlarmEventAcknowledged})
	}))
	res, err := r.api.DrainAgentEffects(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, res.Published)
	require.Len(t, effectRows(t, r.api, ctx), 1)
}

// A kill that lands between the execution's abort and the re-enable must win: the re-enable only
// ever reopens a fence killed at an OLDER generation than the decision that justifies it.
func TestReenableFenceNeverReopensAKillAtTheSameGeneration(t *testing.T) {
	r, _, ctx, _ := executionSetup(t, AlarmProposalAck, "a1")
	v := r.decider.exec.ActuationGeneration
	require.NoError(t, r.api.ApplyKillEvent(ctx, killFor(rigTenant, v)))
	require.NoError(t, r.api.reenableFence(ctx, v))
	var fence AgentActuationFence
	require.NoError(t, r.api.RDB.DB(ctx).First(&fence).Error)
	require.False(t, fence.Enabled)
	require.NoError(t, r.api.reenableFence(ctx, v+1))
	require.NoError(t, r.api.RDB.DB(ctx).First(&fence).Error)
	require.True(t, fence.Enabled)
}
