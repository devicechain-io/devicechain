// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestCommandDisabledApprovalStillAllowsRejection(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	r.api.CommandProposalsEnabled = false
	_, err := r.approve(p)
	require.True(t, actiongate.IsCode(err, "COMMAND_PROPOSALS_DISABLED"))
	require.Empty(t, r.commands(t))
	got, err := r.api.RejectAgentProposal(r.ctx, approver("boss", time.Now()), p.Token, nil)
	require.NoError(t, err)
	require.Equal(t, "REJECTED", got.State)
}
func TestCommandProposalCounters(t *testing.T) {
	for _, mode := range []string{"client-open", "tenant-open", "tenant-hour", "client-hour", "principal-hour"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandRig(t)
			p := r.pending(t)
			count := 20
			code := actiongate.ReasonTooManyOpen
			switch mode {
			case "tenant-open":
				count = 100
				code = "TENANT_OPEN_PROPOSALS_EXCEEDED"
			case "tenant-hour":
				count = 1000
				code = "TENANT_RATE_EXCEEDED"
			case "client-hour":
				count = 200
				code = actiongate.ReasonClientRateExceeded
			case "principal-hour":
				count = 200
				code = actiongate.ReasonPrincipalRateExceeded
			}
			rows := make([]AgentProposal, 0, count-1)
			for i := 1; i < count; i++ {
				row := *p
				row.ID = 0
				row.Token = fmt.Sprintf("seed-%d", i)
				row.RequestId = row.Token
				if mode == "tenant-open" || mode == "tenant-hour" {
					row.ProposerClient = row.Token
					row.ProposerSub = row.Token
				}
				if mode == "principal-hour" {
					row.ProposerClient = row.Token
				}
				if mode == "tenant-hour" || mode == "client-hour" || mode == "principal-hour" {
					row.State = "REFUSED"
				}
				rows = append(rows, row)
			}
			require.NoError(t, r.api.RDB.DB(r.ctx).CreateInBatches(rows, 50).Error)
			out := r.propose(t, commandReq("over"))
			require.Equal(t, ProposalStatusRefused, out.Status)
			require.Nil(t, out.Proposal)
			require.Contains(t, out.Reasons, code)
			var n int64
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Count(&n).Error)
			require.EqualValues(t, count, n)
		})
	}
}
func killForCommands() actiongate.KillEvent {
	return actiongate.KillEvent{EventID: "kill", Tenant: "acme", Cause: actiongate.KillCauseTenantDisabled, ActuationGeneration: 3, At: time.Now().UTC()}
}
func TestCommandKillIsDurableAndAgentOnly(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	for _, status := range []CommandStatus{CommandQueued, CommandHeld, CommandParked, CommandSent, CommandSuccessful} {
		c := &Command{TokenReference: rdb.TokenReference{Token: "agent-" + status.String()}, DeviceToken: "dev-1", Name: "reboot", Status: status.String(), ProposalToken: sql.NullString{String: "proposal-" + status.String(), Valid: true}, CauseKind: sql.NullString{String: CauseKindProposal, Valid: true}}
		if status.Terminal() {
			c.Outcome = sql.NullString{String: OutcomeSucceeded.String(), Valid: true}
		}
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(c).Error)
	}
	_, err := r.api.CreateCommand(r.ctx, &CommandCreateRequest{Token: "ordinary", DeviceToken: "dev-1", Name: "reboot"})
	require.NoError(t, err)
	_, err = r.api.CreateCommand(core.WithTenant(r.ctx, "other"), &CommandCreateRequest{Token: "other", DeviceToken: "dev-1", Name: "reboot"})
	require.NoError(t, err)
	r.api.CommandProposalsEnabled = false
	counts, err := r.api.ApplyKillEventWithCounts(r.ctx, killForCommands())
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.ProposalsKilled)
	require.EqualValues(t, 3, counts.CommandsCancelled)
	require.EqualValues(t, 1, counts.AlreadySent)
	require.Equal(t, "KILLED", r.stored(t, p).State)
	for _, c := range r.commands(t) {
		switch c.Token {
		case "ordinary":
			require.Equal(t, "QUEUED", c.Status)
		case "agent-SENT":
			require.Equal(t, "SENT", c.Status)
		case "agent-SUCCESSFUL":
			require.Equal(t, "SUCCESSFUL", c.Status)
		default:
			require.Equal(t, "CANCELLED", c.Status)
			require.Equal(t, OutcomeNotDelivered.String(), c.Outcome.String)
		}
	}
	require.Error(t, r.api.ApplyKillEvent(core.WithTenant(r.ctx, "other"), killForCommands()))
	r.api.CommandProposalsEnabled = true
	p = r.propose(t, commandReq("after-kill")).Proposal
	_, err = r.approve(p)
	require.ErrorIs(t, err, actiongate.ErrFenceClosed)
	r.dec.exec.ActuationGeneration = 4
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "EXECUTED", got.State)
}
func TestCommandKillRollsBackEveryWrite(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	require.NoError(t, r.api.RDB.Database.Callback().Update().Before("gorm:update").Register("fail-kill", func(db *gorm.DB) {
		if db.Statement.Table == "commands" {
			db.AddError(errors.New("storage failed"))
		}
	}))
	t.Cleanup(func() { r.api.RDB.Database.Callback().Update().Remove("fail-kill") })
	require.Error(t, r.api.ApplyKillEvent(r.ctx, killForCommands()))
	require.Equal(t, "PROPOSED", r.stored(t, p).State)
	var n int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentActuationFence{}).Count(&n).Error)
	require.Zero(t, n)
}
func TestCommandFenceFreshnessAndGeneration(t *testing.T) {
	for _, mode := range []string{"missing-feed", "stale", "kill-before-read"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandRig(t)
			p := r.pending(t)
			switch mode {
			case "missing-feed":
				r.api.KillFeed = nil
			case "stale":
				r.api.KillFeed.Beat(time.Now().Add(-time.Hour))
			case "kill-before-read":
				r.api.beforeFenceForTest = func(tx *gorm.DB) error { return applyKillEventDB(tx, "acme", killForCommands()) }
			}
			_, err := r.approve(p)
			if mode == "kill-before-read" {
				require.ErrorIs(t, err, actiongate.ErrFenceClosed)
			} else {
				require.ErrorIs(t, err, actiongate.ErrFenceUnavailable)
			}
			require.Equal(t, "PROPOSED", r.stored(t, p).State)
			require.Empty(t, r.commands(t))
		})
	}
}
func TestCommandReaperOnlyExpiresDue(t *testing.T) {
	r := newCommandRig(t)
	old := r.pending(t)
	fresh := r.propose(t, commandReq("fresh")).Proposal
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", old.Token).Update("expires_at", time.Now().Add(-time.Hour)).Error)
	r.api.CommandProposalsEnabled = false
	n, err := r.api.ExpireAgentProposals(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, "EXPIRED", r.stored(t, old).State)
	require.Equal(t, "PROPOSED", r.stored(t, fresh).State)
}
func TestCommandTransientDependenciesStoreNothing(t *testing.T) {
	for _, mode := range []string{"target", "nil-target", "policy", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandRig(t)
			switch mode {
			case "forbidden":
				r.targets.err = fmt.Errorf("%w: no device read", auth.ErrForbidden)
			case "target":
				r.targets.err = errors.New("outage")
			case "nil-target":
				r.api.ProposalTargets = nil
			case "policy":
				r.api.AgentDecider = nil
			}
			_, err := r.api.ProposeCommand(r.ctx, rigAgent, commandReq("one"))
			require.Error(t, err)
			if mode == "forbidden" {
				require.ErrorIs(t, err, auth.ErrForbidden)
			} else if mode != "policy" {
				// An upstream outage is a coded, RETRYABLE refusal, never an uncoded error.
				var coded *actiongate.Error
				require.ErrorAs(t, err, &coded)
				require.Equal(t, "VALIDATION_UNAVAILABLE", coded.Code)
				require.True(t, coded.Retryable)
			}
			var n int64
			require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Count(&n).Error)
			require.Zero(t, n)
		})
	}
}
func TestCommandStoredPayloadMustVerifyBeforeIntermediateApproval(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	r.dec.exec = execApprove(2, "boss")
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("payload", datatypes.JSON(`{"v":1}`)).Error)
	_, err := r.approve(p)
	require.Error(t, err)
	require.Zero(t, r.stored(t, p).Revision)
	require.Empty(t, r.commands(t))
}
func TestCommandExpiryBoundsAndFixedRetry(t *testing.T) {
	r := newCommandRig(t)
	now := time.Now().UTC().Truncate(time.Second)
	r.api.proposalNow = func() time.Time { return now }
	for _, tc := range []struct {
		delta time.Duration
		code  string
	}{{time.Minute, "EXPIRES_AT_TOO_SOON"}, {25 * time.Hour, "EXPIRES_AT_TOO_FAR"}} {
		req := commandReq(tc.code)
		s := now.Add(tc.delta).Format(time.RFC3339)
		req.ExpiresAt = &s
		_, err := r.api.ProposeCommand(r.ctx, rigAgent, req)
		require.True(t, actiongate.IsCode(err, tc.code))
	}
	req := commandReq("fixed")
	s := now.Add(2 * time.Minute).Format(time.RFC3339)
	req.ExpiresAt = &s
	p := r.propose(t, req).Proposal
	now = now.Add(time.Hour)
	again := r.propose(t, req)
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, p.ExpiresAt, again.Proposal.ExpiresAt)
}

func TestCommandPassedCutoffAndApprovalExpiry(t *testing.T) {
	for _, cutoffPassed := range []bool{false, true} {
		t.Run(fmt.Sprint(cutoffPassed), func(t *testing.T) {
			r := newCommandRig(t)
			now := time.Now().UTC()
			if cutoffPassed {
				now = now.Add(-2 * time.Hour)
			}
			r.api.proposalNow = func() time.Time { return now }
			p := r.pending(t)
			if cutoffPassed {
				now = time.Now().UTC()
			} else {
				require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("expires_at", time.Now().Add(-time.Minute)).Error)
			}
			got, err := r.approve(p)
			if cutoffPassed {
				require.NoError(t, err)
				require.Equal(t, "REFUSED", got.State)
				require.Contains(t, string(got.Reasons), "COMMAND_CUTOFF_PASSED")
			} else {
				require.True(t, actiongate.IsCode(err, actiongate.CodeExpired))
			}
			require.Empty(t, r.commands(t))
		})
	}
}
func TestCommandEffectPreservesAbsentNullCutoffAndFreshHold(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			r := newCommandRig(t)
			req := commandReq("one")
			null := "null"
			if present {
				req.Payload = &null
			}
			p := r.propose(t, req).Proposal
			hold := &DispatchHold{WindowToken: "window", DispatchAfter: time.Now().Add(time.Minute), DispatchUntil: time.Now().Add(30 * time.Minute)}
			r.targets.target.Hold = hold
			got, err := r.approve(p)
			require.NoError(t, err)
			require.Equal(t, "EXECUTED", got.State)
			rows := r.commands(t)
			require.Len(t, rows, 1)
			c := rows[0]
			if present {
				require.NotNil(t, c.Payload)
				require.Equal(t, "null", string(*c.Payload))
			} else {
				require.Nil(t, c.Payload)
			}
			form, err := verifyCommandProposal(p)
			require.NoError(t, err)
			cutoff, err := time.Parse(time.RFC3339, form.Effective.Cutoff)
			require.NoError(t, err)
			require.True(t, c.ExpiresAt.Time.Equal(cutoff))
			require.Equal(t, "window", c.WindowToken.String)
			require.True(t, c.DispatchAfter.Time.Equal(hold.DispatchAfter))
			require.False(t, c.CauseRuleId.Valid || c.CauseDetectionKind.Valid || c.CauseDetectedAt.Valid)
		})
	}
}
func TestCommandTwoApproversExecute(t *testing.T) {
	r := newCommandRig(t)
	r.targets.target.Impact = "DESTRUCTIVE"
	r.dec.act.RequiredApprovals = 2
	r.dec.exec = execApprove(2, "boss", "carol")
	p := r.pending(t)
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "PROPOSED", got.State)
	got, err = r.api.ApproveAgentProposal(r.ctx, approver("carol", time.Now()), p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, "EXECUTED", got.State)
	require.Len(t, r.commands(t), 1)
}

func TestCommandStoredJSONPresentationDoesNotChangeDigest(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	// JSONB presents insignificant whitespace differently from SQLite.
	for _, col := range []string{"payload", "target_set", "dependencies"} {
		var raw []byte
		switch col {
		case "payload":
			raw = p.Payload
		case "target_set":
			raw = p.TargetSet
		case "dependencies":
			raw = p.Dependencies
		}
		var pretty bytes.Buffer
		require.NoError(t, json.Indent(&pretty, raw, "", "  "))
		require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update(col, datatypes.JSON(pretty.Bytes())).Error)
	}
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "EXECUTED", got.State)
}

// A kill stream replays: the consumer's first start reads the retained kills, and a
// redelivery repeats one. A replayed kill must not end what was proposed, or cancel what
// was queued, after the tenant was re-enabled; a repeat of the kill still in force must.
func TestCommandReplayedKillIsStale(t *testing.T) {
	agentCommand := func(t *testing.T, r *commandRig, token string) {
		t.Helper()
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(&Command{TokenReference: rdb.TokenReference{Token: token}, DeviceToken: "dev-1", Name: "reboot", Status: CommandQueued.String(), ProposalToken: sql.NullString{String: "proposal-" + token, Valid: true}, CauseKind: sql.NullString{String: CauseKindProposal, Valid: true}}).Error)
	}
	status := func(t *testing.T, r *commandRig, token string) string {
		t.Helper()
		for _, c := range r.commands(t) {
			if c.Token == token {
				return c.Status
			}
		}
		t.Fatalf("command %s not found", token)
		return ""
	}
	fence := func(t *testing.T, r *commandRig) AgentActuationFence {
		t.Helper()
		var f AgentActuationFence
		require.NoError(t, r.api.RDB.DB(r.ctx).First(&f).Error)
		return f
	}
	kill := func(gen int) actiongate.KillEvent {
		ev := killForCommands()
		ev.ActuationGeneration = gen
		ev.EventID = fmt.Sprintf("kill-%d", gen)
		return ev
	}

	t.Run("replay after re-enable", func(t *testing.T) {
		r := newCommandRig(t)
		_, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(3))
		require.NoError(t, err)
		require.NoError(t, r.api.reenableFence(r.ctx, 4))
		p := r.pending(t)
		agentCommand(t, r, "after-reenable")
		for _, gen := range []int{3, 2} {
			counts, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(gen))
			require.NoError(t, err)
			require.True(t, counts.Stale, "generation %d", gen)
			require.Zero(t, counts.ProposalsKilled+counts.CommandsCancelled)
		}
		require.Equal(t, "PROPOSED", r.stored(t, p).State)
		require.Equal(t, "QUEUED", status(t, r, "after-reenable"))
		require.True(t, fence(t, r).Enabled, "a replayed kill must not close a re-enabled fence")
	})
	t.Run("repeat while still killed applies", func(t *testing.T) {
		r := newCommandRig(t)
		_, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(3))
		require.NoError(t, err)
		agentCommand(t, r, "during-kill")
		counts, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(3))
		require.NoError(t, err)
		require.False(t, counts.Stale)
		require.EqualValues(t, 1, counts.CommandsCancelled)
		require.Equal(t, "CANCELLED", status(t, r, "during-kill"))
		require.False(t, fence(t, r).Enabled)
	})
	t.Run("newer kill after re-enable applies", func(t *testing.T) {
		r := newCommandRig(t)
		_, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(3))
		require.NoError(t, err)
		require.NoError(t, r.api.reenableFence(r.ctx, 4))
		p := r.pending(t)
		counts, err := r.api.ApplyKillEventWithCounts(r.ctx, kill(4))
		require.NoError(t, err)
		require.False(t, counts.Stale)
		require.Equal(t, "KILLED", r.stored(t, p).State)
		require.False(t, fence(t, r).Enabled)
	})
}

// Ruled retention: a decided proposal (and so its request key) goes after 30 days, in every
// tenant; a PROPOSED one never goes by age, and a younger decided one stays.
func TestCommandPurgeRemovesOnlyOldDecidedProposals(t *testing.T) {
	r := newCommandRig(t)
	old := time.Now().UTC().Add(-ProposalRetention - time.Hour)
	young := time.Now().UTC().Add(-ProposalRetention + time.Hour)
	seed := func(tenant, token, state string, created time.Time, decided ...time.Time) {
		t.Helper()
		var decidedAt *time.Time
		if state != "PROPOSED" {
			d := created
			if len(decided) > 0 {
				d = decided[0]
			}
			decidedAt = &d
		}
		row := AgentProposal{Model: gorm.Model{CreatedAt: created, UpdatedAt: created}, Record: actiongate.Record{Token: token, EffectToken: "e-" + token, RequestId: token, Kind: string(actiongate.KindCommandSend), State: state, Payload: datatypes.JSON("{}"), TargetSet: datatypes.JSON("[]"), Dependencies: datatypes.JSON("{}"), Digest: "d", Impact: "NORMAL", TargetCount: 1, ProposerSub: rigAgent.Principal, ProposerClient: rigAgent.Client, Approvals: datatypes.JSON("[]"), Reasons: datatypes.JSON("[]"), ExpiresAt: created, DecidedAt: decidedAt}}
		require.NoError(t, r.api.RDB.DB(core.WithTenant(r.ctx, tenant)).Create(&row).Error)
	}
	for _, tenant := range []string{"acme", "other"} {
		for _, state := range []string{"EXECUTED", "REFUSED", "REJECTED", "EXPIRED", "KILLED"} {
			seed(tenant, tenant+"-old-"+state, state, old)
		}
		seed(tenant, tenant+"-old-proposed", "PROPOSED", old)
		seed(tenant, tenant+"-young-executed", "EXECUTED", young)
		// Created long ago but decided recently (a late decision, or an expiry the reaper
		// reached only after an outage): the retention clock starts at the decision.
		seed(tenant, tenant+"-old-but-just-decided", "EXPIRED", old, young)
	}
	n, err := r.api.PurgeAgentProposals(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 10, n)
	for _, tenant := range []string{"acme", "other"} {
		var left []AgentProposal
		require.NoError(t, r.api.RDB.DB(core.WithTenant(r.ctx, tenant)).Unscoped().Order("token").Find(&left).Error)
		var tokens []string
		for _, p := range left {
			tokens = append(tokens, p.Token)
		}
		require.Equal(t, []string{tenant + "-old-but-just-decided", tenant + "-old-proposed", tenant + "-young-executed"}, tokens, "a soft delete would leave the purged rows in the table")
	}
	// The released key really is free: the same requestId proposes again.
	r.api.proposalNow = nil
	out := r.propose(t, commandReq("acme-old-EXECUTED"))
	require.Equal(t, ProposalStatusPending, out.Status)
}

// Defence in depth behind the kill consumer: a killed tenant's agent command is never
// claimed for dispatch, even when its cancellation has not landed (or the kill was lost in
// delivery and the feed latched). Ordinary commands are untouched by any of it.
func TestAgentCommandDispatchRefusedWhileKilledOrFeedUnknown(t *testing.T) {
	for _, mode := range []string{"open", "fence-closed", "fence-missing", "feed-stale", "feed-latched"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandRig(t)
			mk := func(token string, agent bool) *Command {
				t.Helper()
				c := &Command{TokenReference: rdb.TokenReference{Token: token}, DeviceToken: "dev-1", Name: "reboot", Status: CommandQueued.String(), QueuedTime: time.Now().UTC()}
				if agent {
					c.ProposalToken = sql.NullString{String: "proposal-" + token, Valid: true}
					c.CauseKind = sql.NullString{String: CauseKindProposal, Valid: true}
				}
				require.NoError(t, r.api.RDB.DB(r.ctx).Create(c).Error)
				return c
			}
			agent, ordinary := mk("agent", true), mk("ordinary", false)
			if mode != "fence-missing" {
				require.NoError(t, r.api.RDB.DB(r.ctx).Create(&AgentActuationFence{Enabled: true, Epoch: 1, UpdatedAt: time.Now().UTC()}).Error)
			}
			switch mode {
			case "fence-closed":
				require.NoError(t, applyKillEventDB(r.api.RDB.DB(r.ctx), "acme", killForCommands()))
			case "feed-stale":
				r.api.KillFeed.Beat(time.Now().Add(-time.Hour))
			case "feed-latched":
				r.api.KillFeed.Latch()
				r.api.KillFeed.Beat(time.Now()) // a later idle poll must not vouch for it again
			}
			_, won, err := r.api.MarkSent(r.ctx, agent.ID)
			require.NoError(t, err)
			require.Equal(t, mode == "open", won, "agent command claimed")
			if mode != "open" {
				// The other claim path is refused too (in "open" the row is already SENT).
				_, won, err = r.api.MarkSentByToken(r.ctx, agent.Token)
				require.NoError(t, err)
				require.False(t, won, "agent command claimed by token")
			}
			_, won, err = r.api.MarkSent(r.ctx, ordinary.ID)
			require.NoError(t, err)
			require.True(t, won, "an ordinary command is never held by the agent fence")
		})
	}
}

// Dry runs store nothing, but each costs a policy decision and a device read: they have their
// own budget per agent client and, independently, per person.
func TestCommandDryRunsAreRateLimited(t *testing.T) {
	r := newCommandRig(t)
	now := time.Now().UTC()
	r.api.proposalNow = func() time.Time { return now }
	dry := func(p actiongate.Proposer) error {
		req := commandReq("dry")
		req.DryRun = true
		_, err := r.api.ProposeCommand(r.ctx, p, req)
		return err
	}
	for i := 0; i < DryRunsPerMinute; i++ {
		require.NoError(t, dry(rigAgent))
	}
	err := dry(rigAgent)
	var coded *actiongate.Error
	require.ErrorAs(t, err, &coded)
	require.Equal(t, "DRY_RUN_RATE_EXCEEDED", coded.Code)
	require.True(t, coded.Retryable)
	otherClient := rigAgent
	otherClient.Client = "another-client"
	require.Error(t, dry(otherClient), "the same person through another client shares the person's budget")
	stranger := actiongate.Proposer{Tenant: "acme", Principal: "someone-else", Client: "their-client", Sid: "s"}
	require.NoError(t, dry(stranger))
	// A stored proposal is not a dry run and is governed by the ledger's own limits.
	require.Equal(t, ProposalStatusPending, r.propose(t, commandReq("stored")).Status)
	// The window ends.
	now = now.Add(time.Minute)
	require.NoError(t, dry(rigAgent))
}

// An EXISTING answer reports APPROVE only while the proposal still awaits approval.
func TestCommandExistingVerdictFollowsState(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	again := r.propose(t, commandReq("one"))
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, actiongate.VerdictApprove, again.Verdict)
	_, err := r.api.RejectAgentProposal(r.ctx, approver("boss", time.Now()), p.Token, nil)
	require.NoError(t, err)
	again = r.propose(t, commandReq("one"))
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, actiongate.VerdictRefuse, again.Verdict, "a rejected proposal is not pending approval")
	require.Zero(t, again.RequiredApprovals)
}

// A command name carries no control characters (NUL included).
func TestCommandNameMustBePrintable(t *testing.T) {
	r := newCommandRig(t)
	for i, bad := range []string{"re\x00boot", "re\x01boot", "reboot\n", "re\u0085boot", "re‮boot"} {
		// Each on its own requestId and as a dry run, so nothing but the name can refuse it.
		req := commandReq(fmt.Sprintf("bad-%d", i))
		req.DryRun = true
		req.Name = bad
		_, err := r.api.ProposeCommand(r.ctx, rigAgent, req)
		require.ErrorContains(t, err, "printable", "%q must be refused by the name check itself", bad)
	}
}

// A blocked agent command must not starve the ordinary commands queued behind it: the
// candidate reads (pull, nudge, sweep) skip it before ORDER BY id LIMIT, not only the claim.
func TestBlockedAgentCommandDoesNotStarveOrdinaryOnes(t *testing.T) {
	r := newCommandRig(t)
	r.api.KillFeed.Beat(time.Now().Add(-time.Hour)) // stale: every agent row is blocked
	mk := func(token string, agent bool) {
		t.Helper()
		c := &Command{TokenReference: rdb.TokenReference{Token: token}, DeviceToken: "dev-1", Name: "reboot", Status: CommandQueued.String(), QueuedTime: time.Now().UTC()}
		if agent {
			c.ProposalToken = sql.NullString{String: "proposal-" + token, Valid: true}
			c.CauseKind = sql.NullString{String: CauseKindProposal, Valid: true}
		}
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(c).Error)
	}
	mk("older-agent", true)
	mk("ordinary", false)
	queued, err := r.api.QueuedCommandsForDevice(r.ctx, "dev-1", 1)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "ordinary", queued[0].Token)
	pending, err := r.api.PendingCommands(r.ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "ordinary", pending[0].Token)
	pulled, err := r.api.PullCommands(r.ctx, "dev-1", 1)
	require.NoError(t, err)
	require.Len(t, pulled, 1, "a pull of one must reach the ordinary command behind the blocked agent one")
	require.Equal(t, "ordinary", pulled[0].Command.Token)
}
