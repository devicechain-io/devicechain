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
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The alarm proposal ledger, driven through the Api over SQLite with a scripted decider standing
// in for user-management. The decider is the ONLY source of verdicts here, so every test states
// what the tenant's policy answered.

type fakeDecider struct {
	act         actiongate.ActuationDecision
	actErr      error
	exec        actiongate.ExecutionDecision
	execErr     error
	actCalls    int
	execReqs    []actiongate.ExecutionRequest
	execBearers []string
	second      func() (actiongate.ExecutionDecision, error)
}

func (f *fakeDecider) Actuation(context.Context, actiongate.ActuationRequest) (actiongate.ActuationDecision, error) {
	f.actCalls++
	return f.act, f.actErr
}

func (f *fakeDecider) Execution(ctx context.Context, req actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	f.execReqs = append(f.execReqs, req)
	bearer, _ := auth.BearerFromContext(ctx)
	f.execBearers = append(f.execBearers, bearer)
	if len(f.execReqs) == 2 && f.second != nil {
		return f.second()
	}
	return f.exec, f.execErr
}

func approveOne() actiongate.ActuationDecision {
	return actiongate.ActuationDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, PolicyVersion: 4,
		Reasons: []string{}, Expiry: time.Hour}
}

type proposalRig struct {
	api     *Api
	db      *gorm.DB
	decider *fakeDecider
}

const rigTenant = "acme"

var (
	rigAgent = actiongate.Proposer{Tenant: rigTenant, Principal: "ana@example.invalid", Client: "agent-desktop", Sid: "sid-1"}
	rigOther = actiongate.Proposer{Tenant: rigTenant, Principal: "ana@example.invalid", Client: "other-agent", Sid: "sid-2"}
)

func newProposalRig(t *testing.T) *proposalRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&Alarm{}, &AgentProposal{}, &rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(db))
	d := &fakeDecider{act: approveOne()}
	api := NewApi(&rdb.RdbManager{Database: db})
	api.AgentDecider = d
	r := &proposalRig{api: api, db: db, decider: d}
	r.alarm(t, "a1", AlarmStateActive)
	r.alarm(t, "a2", AlarmStateCleared)
	return r
}

func (r *proposalRig) alarm(t *testing.T, token string, state AlarmState) {
	t.Helper()
	require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Create(&Alarm{
		TokenReference: rdb.TokenReference{Token: token}, OriginatorType: string(entity.TypeDevice), OriginatorId: 1,
		AlarmKey: token + "-key", MetricKey: "temp", State: string(state), Severity: string(AlarmSeverityMajor),
		RaisedTime: time.Now().Add(-time.Hour),
	}).Error)
}

func (r *proposalRig) propose(t *testing.T, p actiongate.Proposer, req AlarmProposalRequest) *ProposalOutcome {
	t.Helper()
	out, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), p, req)
	require.NoError(t, err)
	return out
}

func clearReq(id string, tokens ...string) AlarmProposalRequest {
	return AlarmProposalRequest{Action: AlarmProposalClear, AlarmTokens: tokens, RequestId: id}
}

// approver is a person with a TOTP sign-in verified at mfa.
func approver(name string, mfa time.Time) actiongate.Approver {
	c := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: rigTenant, Username: name,
		Authorities: []string{string(auth.AlarmWrite)}, Amr: auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(mfa)}
	at := mfa
	return actiongate.Approver{Principal: name, Amr: c.Amr, MfaTime: &at, Claims: c}
}

func (r *proposalRig) stored(t *testing.T, token string) *AgentProposal {
	t.Helper()
	p, err := r.api.AgentProposalByToken(tenantCtx(rigTenant), token, nil)
	require.NoError(t, err)
	return p
}

func execApprove(required int, valid ...string) actiongate.ExecutionDecision {
	return actiongate.ExecutionDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: required, ValidApprovers: valid,
		StepUp: auth.StepUp{MaxAge: 15 * time.Minute}, AllowSelfConfirm: required == 1, PolicyVersion: 4}
}

// A proposal is stored with its canonical action, frozen targets, digest, proposer and client,
// and nothing about any alarm changes.
func TestProposalIsRecordedNotExecuted(t *testing.T) {
	r := newProposalRig(t)
	why := "the freezer was defrosting"
	req := clearReq("r1", "a2", "a1", "a1")
	req.Rationale = &why
	out := r.propose(t, rigAgent, req)
	require.Equal(t, ProposalStatusPending, out.Status)
	require.Equal(t, actiongate.VerdictApprove, out.Verdict)
	require.Equal(t, 2, out.TargetCount, "duplicates count once")
	require.Len(t, out.Preview, 2)
	require.True(t, out.Preview[0].HidesLiveCondition, "clearing an ACTIVE alarm hides a live condition")
	require.False(t, out.Preview[1].HidesLiveCondition)

	p := r.stored(t, out.Proposal.Token)
	require.Equal(t, string(actiongate.StateProposed), p.State)
	require.Equal(t, `{"action":"CLEAR","alarms":["a1","a2"]}`, string(p.Payload))
	require.Equal(t, `["a1","a2"]`, string(p.TargetSet))
	require.Equal(t, actiongate.Digest(actiongate.KindAlarmClear, p.Payload, p.TargetSet), p.Digest)
	require.Equal(t, out.Digest, p.Digest)
	require.Equal(t, "ana@example.invalid", p.ProposerSub)
	require.Equal(t, "agent-desktop", p.ProposerClient)
	require.Equal(t, "sid-1", *p.ProposerSid)
	require.Equal(t, why, *p.Rationale)
	require.Equal(t, 4, p.PolicyVersion)
	require.NotEqual(t, p.Token, p.EffectToken)

	var alarms []Alarm
	require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Order("token").Find(&alarms).Error)
	require.Equal(t, string(AlarmStateActive), alarms[0].State, "a proposal changes no alarm")
}

// The same requestId with the same request returns the original; with another request, CONFLICT.
func TestRequestIdReuseWithDifferentPayloadConflicts(t *testing.T) {
	r := newProposalRig(t)
	first := r.propose(t, rigAgent, clearReq("r1", "a1"))
	again := r.propose(t, rigAgent, clearReq("r1", "a1"))
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, first.Proposal.Token, again.Proposal.Token)

	_, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, clearReq("r1", "a2"))
	require.True(t, conflict.Is(err), "a reused requestId for a different request must be CONFLICT, got %v", err)
	ack := clearReq("r1", "a1")
	ack.Action = AlarmProposalAck
	_, err = r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, ack)
	require.True(t, conflict.Is(err), "a different action under the same requestId must be CONFLICT")

	// Another client's requestId space is its own.
	other := r.propose(t, rigOther, clearReq("r1", "a2"))
	require.Equal(t, ProposalStatusPending, other.Status)
}

// A dry run decides and checks, and stores nothing.
func TestDryRunStoresNothing(t *testing.T) {
	r := newProposalRig(t)
	req := clearReq("r1", "a1")
	req.DryRun = true
	out := r.propose(t, rigAgent, req)
	require.Equal(t, ProposalStatusDryRun, out.Status)
	require.Nil(t, out.Proposal)
	require.NotNil(t, out.ExpiresAt)
	var n int64
	require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Model(&AgentProposal{}).Count(&n).Error)
	require.Zero(t, n)
}

// Every verdict but APPROVE is a refusal, stored for the record and never waiting; AUTO is refused
// too (no automatic path in this release); and an alarm that does not exist refuses.
func TestEveryVerdictButApproveIsARefusal(t *testing.T) {
	for name, tc := range map[string]struct {
		act    actiongate.ActuationDecision
		tokens []string
		reason string
	}{
		"refuse":  {act: actiongate.ActuationDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"CLIENT_NOT_ALLOWED"}}, tokens: []string{"a1"}, reason: "CLIENT_NOT_ALLOWED"},
		"auto":    {act: actiongate.ActuationDecision{Verdict: actiongate.VerdictAuto, Expiry: time.Hour}, tokens: []string{"a1"}, reason: actiongate.ReasonAutoNotAvailable},
		"missing": {act: approveOne(), tokens: []string{"a1", "nope"}, reason: ReasonAlarmNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			r := newProposalRig(t)
			r.decider.act = tc.act
			out := r.propose(t, rigAgent, clearReq("r1", tc.tokens...))
			require.Equal(t, ProposalStatusRefused, out.Status)
			require.Equal(t, actiongate.VerdictRefuse, out.Verdict)
			require.Zero(t, out.RequiredApprovals)
			require.Contains(t, out.Reasons, tc.reason)
			p := r.stored(t, out.Proposal.Token)
			require.Equal(t, string(actiongate.StateRefused), p.State)
			_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), p.Token, p.Digest)
			require.ErrorIs(t, err, actiongate.ErrNotPending, "a refused proposal can never be approved")
		})
	}
}

// An unreachable or unreadable policy refuses and stores nothing; so does an area with no decider.
func TestPolicyUnavailableRefuses(t *testing.T) {
	r := newProposalRig(t)
	r.decider.actErr = errors.New("connection refused")
	_, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, clearReq("r1", "a1"))
	require.True(t, actiongate.IsCode(err, actiongate.CodePolicyUnavailable), "got %v", err)
	r.api.AgentDecider = nil
	_, err = r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, clearReq("r2", "a1"))
	require.True(t, actiongate.IsCode(err, actiongate.CodePolicyUnavailable), "got %v", err)
	var n int64
	require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Model(&AgentProposal{}).Count(&n).Error)
	require.Zero(t, n)
}

// The counters: the policy's rate per client and per principal, and the open-proposal cap. A
// refusal at a counter is not stored.
func TestCountersRefuseOverTheirLimits(t *testing.T) {
	r := newProposalRig(t)
	r.decider.act.ActionsPerHour = 2
	r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.propose(t, rigAgent, clearReq("r2", "a1"))
	out := r.propose(t, rigAgent, clearReq("r3", "a1"))
	require.Equal(t, ProposalStatusRefused, out.Status)
	require.Nil(t, out.Proposal, "a counter refusal is not stored")
	require.Contains(t, out.Reasons, actiongate.ReasonClientRateExceeded)

	// Another client of the SAME person is held by the per-principal count.
	out = r.propose(t, rigOther, clearReq("r4", "a1"))
	require.Contains(t, out.Reasons, actiongate.ReasonPrincipalRateExceeded)

	// The open-proposal cap holds at the platform rate.
	r2 := newProposalRig(t)
	for i := 0; i < actiongate.DefaultOpenPerClient; i++ {
		require.Equal(t, ProposalStatusPending, r2.propose(t, rigAgent, clearReq(string(rune('A'+i)), "a1")).Status)
	}
	out = r2.propose(t, rigAgent, clearReq("over", "a1"))
	require.Contains(t, out.Reasons, actiongate.ReasonTooManyOpen)
}

// The digest an approver names must be the stored one.
func TestApprovalDigestMismatchRefused(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = execApprove(2, "boss")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token,
		actiongate.Digest(actiongate.KindAlarmClear, []byte(`{"action":"CLEAR","alarms":["a2"]}`), []byte(`["a2"]`)))
	require.ErrorIs(t, err, ErrProposalDigestMismatch)
	require.Empty(t, r.decider.execReqs, "nothing is asked for a proposal the approver was not shown")
}

// An expired proposal cannot be approved, whether or not the reaper has run.
func TestExpiredApprovalCannotExecute(t *testing.T) {
	r := newProposalRig(t)
	r.decider.act.Expiry = time.Millisecond
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	time.Sleep(5 * time.Millisecond)
	r.decider.exec = execApprove(1, "boss")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeExpired), "got %v", err)
	require.Empty(t, r.decider.execReqs, "nothing is asked about a proposal whose window has closed")

	n, err := r.api.ExpireAgentProposals(core.WithSystemContext(context.Background()))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, string(actiongate.StateExpired), r.stored(t, out.Proposal.Token).State)
}

// A refusal at approval time (a kill switch, a revoked proposer, a delisted client) ends the
// proposal as REFUSED with the reasons; it can never be approved afterwards.
func TestKillSwitchRefusesApprovedUnexecuted(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"TENANT_ACTUATION_OFF"}}
	p, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateRefused), p.State)
	require.JSONEq(t, `["TENANT_ACTUATION_OFF"]`, string(p.Reasons))
	require.Equal(t, "boss", *p.DecidedBy)

	r.decider.exec = execApprove(1, "boss")
	_, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.ErrorIs(t, err, actiongate.ErrNotPending)
}

// The decision is asked about the ORIGINAL proposer and client, recorded on the proposal, and about
// every approval so far plus this one.
func TestRevokedProposerProposalRefusedAtExecution(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = actiongate.ExecutionDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"PROPOSER_LOST_AUTHORITY"}}
	p, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateRefused), p.State)
	require.JSONEq(t, `["PROPOSER_NOT_IN_STANDING"]`, string(p.Reasons))
	require.JSONEq(t, `["PROPOSER_NOT_IN_STANDING"]`, string(r.stored(t, p.Token).Reasons))
	require.Len(t, r.decider.execReqs, 1)
	got := r.decider.execReqs[0]
	require.Equal(t, actiongate.ExecutionRequest{ProposalToken: p.Token, Kind: actiongate.KindAlarmClear, Impact: "NORMAL",
		TargetCount: 1, ProposerPrincipal: "ana@example.invalid", ProposerClient: "agent-desktop", ProposerSid: "sid-1",
		Approvers: []string{"boss"}}, got)
}

// A policy tightened to two approvers after the proposal keeps it pending: the first approval is
// recorded with the new requirement.
func TestPolicyTightenedToTwoKeepsPending(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	require.Equal(t, 1, out.Proposal.RequiredApprovals)
	r.decider.exec = execApprove(2, "boss", "carol")
	p, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateProposed), p.State)
	require.Equal(t, 2, p.RequiredApprovals)
	approvals, err := actiongate.ParseApprovals(p.Approvals)
	require.NoError(t, err)
	require.Equal(t, []string{"boss"}, actiongate.Principals(approvals))

	// The same person approving again is a no-op, not a second approval.
	again, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, p.Revision, again.Revision)
	require.Len(t, r.decider.execReqs, 2, "idempotency still asks the current decision")
	require.Equal(t, []string{"boss"}, r.decider.execReqs[1].Approvers)

	// The second approver executes it.
	p, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("carol", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), p.State)
	approvals, err = actiongate.ParseApprovals(p.Approvals)
	require.NoError(t, err)
	require.Equal(t, []string{"boss", "carol"}, actiongate.Principals(approvals))
}

// A first approver who has since lost their standing no longer counts toward the two.
func TestRevokedFirstApproverNoLongerCounts(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	r.decider.exec = execApprove(2, "boss", "carol")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)

	// boss is no longer valid: carol's approval is the only one that stands, so it is recorded
	// rather than completing the proposal.
	r.decider.exec = execApprove(2, "carol")
	p, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("carol", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateProposed), p.State)
	require.Equal(t, []string{"boss", "carol"}, r.decider.execReqs[1].Approvers, "every approver so far is re-checked")
	r.decider.exec = execApprove(2, "carol", "dave")
	_, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("dave", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.Equal(t, []string{"boss", "carol", "dave"}, r.decider.execReqs[2].Approvers)
}

// The approver must be one of the valid approvers, fresh by the policy's step-up, and may be the
// proposer only for a single approval the policy lets them confirm.
func TestApproverRulesAreTheCurrentPolicys(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	ctx := tenantCtx(rigTenant)

	r.decider.exec = execApprove(2, "carol")
	_, err := r.api.ApproveAgentProposal(ctx, approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeNotAnApprover), "got %v", err)

	r.decider.exec = execApprove(2, "boss")
	_, err = r.api.ApproveAgentProposal(ctx, approver("boss", time.Now().Add(-time.Hour)), out.Proposal.Token, out.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeStepUpRequired), "got %v", err)

	r.decider.exec = execApprove(2, "ana@example.invalid")
	// Even a decision that (wrongly) allowed self-confirmation never lets the proposer be one of two.
	r.decider.exec.AllowSelfConfirm = true
	_, err = r.api.ApproveAgentProposal(ctx, approver("ana@example.invalid", time.Now()), out.Proposal.Token, out.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeSelfApproval), "the proposer never approves a two-person proposal")

	r.decider.exec = execApprove(1, "ana@example.invalid")
	r.decider.exec.AllowSelfConfirm = false
	_, err = r.api.ApproveAgentProposal(ctx, approver("ana@example.invalid", time.Now()), out.Proposal.Token, out.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeSelfApproval), "self-confirm forbidden by policy")

	r.decider.exec = execApprove(1, "ana@example.invalid")
	_, err = r.api.ApproveAgentProposal(ctx, approver("ana@example.invalid", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err, "control: self-confirm of a single approval executes")
}

// A rejection ends the proposal; nothing decides it afterwards.
func TestRejectEndsTheProposal(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	note := "not now"
	p, err := r.api.RejectAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, &note)
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateRejected), p.State)
	require.Equal(t, note, *p.Note)
	_, err = r.api.RejectAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, nil)
	require.ErrorIs(t, err, actiongate.ErrNotPending)
	r.decider.exec = execApprove(2, "boss")
	_, err = r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.ErrorIs(t, err, actiongate.ErrNotPending)
}

// Another tenant cannot read, approve or reject a proposal; an agent sees only its own.
func TestCrossTenantProposalReadRefused(t *testing.T) {
	r := newProposalRig(t)
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	other := tenantCtx("globex")
	_, err := r.api.AgentProposalByToken(other, out.Proposal.Token, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = r.api.RejectAgentProposal(other, approver("boss", time.Now()), out.Proposal.Token, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	_, err = r.api.AgentProposalByToken(tenantCtx(rigTenant), out.Proposal.Token, &rigOther)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "another client of the same person does not see it")
	mine, err := r.api.AgentProposals(tenantCtx(rigTenant), AgentProposalSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}, &rigAgent)
	require.NoError(t, err)
	require.Len(t, mine.Results, 1)
	theirs, err := r.api.AgentProposals(tenantCtx(rigTenant), AgentProposalSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10}}, &rigOther)
	require.NoError(t, err)
	require.Empty(t, theirs.Results)
}

// Every ledger write leaves its journal row: the proposal and each transition.
func TestEveryLedgerTransitionJournaled(t *testing.T) {
	r := newProposalRig(t)
	count := func() int64 {
		var n int64
		require.NoError(t, r.db.WithContext(core.WithSystemContext(context.Background())).Model(&rdb.AuditEvent{}).
			Where("table_name LIKE ?", "%agent_proposals").Count(&n).Error)
		return n
	}
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	require.EqualValues(t, 1, count(), "the proposal")
	r.decider.exec = execApprove(2, "boss", "carol")
	_, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
	require.NoError(t, err)
	require.EqualValues(t, 2, count(), "the approval")
	_, err = r.api.RejectAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, nil)
	require.NoError(t, err)
	require.EqualValues(t, 3, count(), "the rejection")
}

// The request is bounded and normalized before anything is read or asked.
func TestAlarmProposalRequestIsBounded(t *testing.T) {
	r := newProposalRig(t)
	for name, req := range map[string]AlarmProposalRequest{
		"no alarms":    {Action: AlarmProposalAck, RequestId: "r"},
		"bad action":   {Action: "DELETE", AlarmTokens: []string{"a1"}, RequestId: "r"},
		"no requestId": {Action: AlarmProposalAck, AlarmTokens: []string{"a1"}},
		"control char": {Action: AlarmProposalAck, AlarmTokens: []string{"a1"}, RequestId: "r\n1"},
		"too many":     {Action: AlarmProposalAck, AlarmTokens: make([]string, MaxProposalAlarms+1), RequestId: "r"},
	} {
		_, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, req)
		require.Error(t, err, name)
	}
	long := string(make([]byte, MaxProposalRationale+1))
	_, err := r.api.ProposeAlarmAction(tenantCtx(rigTenant), rigAgent, AlarmProposalRequest{Action: AlarmProposalAck,
		AlarmTokens: []string{"a1"}, RequestId: "r", Rationale: &long})
	require.Error(t, err)
	require.Zero(t, r.decider.actCalls, "nothing is asked for a malformed request")
}

// A prior factor must still be fresh at completion; re-approving replaces a stale entry.
func TestApprovalFreshnessAtCompletion(t *testing.T) {
	for _, mode := range []string{"stale", "fresh", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			r := newProposalRig(t)
			out := r.propose(t, rigAgent, clearReq("r1", "a1"))
			r.decider.exec = execApprove(2, "boss", "carol")
			p, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", time.Now()), out.Proposal.Token, out.Digest)
			require.NoError(t, err)
			if mode != "fresh" {
				approvals, err := actiongate.ParseApprovals(p.Approvals)
				require.NoError(t, err)
				old := time.Now().Add(-20 * time.Minute)
				approvals[0].MfaTime = &old
				raw, err := json.Marshal(approvals)
				require.NoError(t, err)
				require.NoError(t, r.db.WithContext(tenantCtx(rigTenant)).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("approvals", raw).Error)
			}
			if mode == "refresh" {
				fresh := time.Now().UTC()
				again, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("boss", fresh), out.Proposal.Token, out.Digest)
				require.NoError(t, err)
				require.Equal(t, p.Revision+1, again.Revision)
				approvals, err := actiongate.ParseApprovals(again.Approvals)
				require.NoError(t, err)
				require.Len(t, approvals, 1)
				require.Equal(t, "boss", approvals[0].Sub)
				require.True(t, fresh.Equal(*approvals[0].MfaTime))
				require.Equal(t, []string{"boss"}, r.decider.execReqs[len(r.decider.execReqs)-1].Approvers)
			}
			got, err := r.api.ApproveAgentProposal(tenantCtx(rigTenant), approver("carol", time.Now()), out.Proposal.Token, out.Digest)
			if mode == "stale" {
				require.NoError(t, err)
				require.Equal(t, string(actiongate.StateProposed), got.State)
				require.Equal(t, p.Revision+1, got.Revision)
				approvals, err := actiongate.ParseApprovals(got.Approvals)
				require.NoError(t, err)
				require.Equal(t, []string{"boss", "carol"}, actiongate.Principals(approvals))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCoarseProposalReasons(t *testing.T) {
	require.Equal(t, []string{"FIRST", actiongate.ReasonProposerNotInStanding, "LAST"}, actiongate.PersistedReasons([]string{
		"FIRST", "PROPOSER_SESSION_ENDED", "PROPOSER_NOT_A_MEMBER", "PROPOSER_SCOPED", "PROPOSER_LOST_AUTHORITY", "PROPOSER_CLIENT_REFUSED", "PROPOSER_FUTURE", "LAST", "FIRST",
	}))
}

func TestProposedDecisionReasonsAreCoarsenedBeforeStorage(t *testing.T) {
	r := newProposalRig(t)
	r.decider.act = actiongate.ActuationDecision{Verdict: actiongate.VerdictRefuse, Reasons: []string{"FIRST", "PROPOSER_FUTURE", "PROPOSER_SESSION_ENDED", "LAST"}}
	out := r.propose(t, rigAgent, clearReq("r1", "a1"))
	require.Equal(t, []string{"FIRST", actiongate.ReasonProposerNotInStanding, "LAST"}, out.Reasons)
	require.JSONEq(t, `["FIRST","PROPOSER_NOT_IN_STANDING","LAST"]`, string(r.stored(t, out.Proposal.Token).Reasons))
}
