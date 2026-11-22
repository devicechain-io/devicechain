// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type fakeDecider struct {
	act             actiongate.ActuationDecision
	exec            actiongate.ExecutionDecision
	actErr, execErr error
	actCalls        int
	execReqs        []actiongate.ExecutionRequest
	second          func() (actiongate.ExecutionDecision, error)
	beforeExec      func()
}

func (d *fakeDecider) Actuation(_ context.Context, req actiongate.ActuationRequest) (actiongate.ActuationDecision, error) {
	d.actCalls++
	if req.Kind != actiongate.KindCommandSend || req.Command != "reboot" || req.TargetCount != 1 {
		return actiongate.ActuationDecision{}, errors.New("lost command policy context")
	}
	return d.act, d.actErr
}
func (d *fakeDecider) Execution(ctx context.Context, req actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	if _, deadline := ctx.Deadline(); !deadline && d.beforeExec != nil {
		d.beforeExec()
	}
	d.execReqs = append(d.execReqs, req)
	if len(d.execReqs) == 2 && d.second != nil {
		return d.second()
	}
	return d.exec, d.execErr
}
func execApprove(n int, valid ...string) actiongate.ExecutionDecision {
	return actiongate.ExecutionDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: n, ValidApprovers: valid, ActuationGeneration: 3, StepUp: auth.StepUp{MaxAge: 15 * time.Minute}, AllowSelfConfirm: n == 1}
}

var rigAgent = actiongate.Proposer{Tenant: "acme", Principal: "agent-person", Client: "agent-client", Sid: "grant"}

func approver(name string, at time.Time) actiongate.Approver {
	c := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: name, SessionID: auth.SessionID(name + "-sid"), Authorities: []string{string(auth.CommandWrite)}, Amr: auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(at)}
	return actiongate.Approver{Principal: name, Sid: string(c.SessionID), Claims: c, Amr: c.Amr, MfaTime: &at}
}

type fakeTargets struct {
	target  ProposalTarget
	err     error
	calls   int
	payload []byte
	bearers []string
}

func (f *fakeTargets) ReadProposalTarget(ctx context.Context, _ string, _ string, p []byte, _ time.Time) (*ProposalTarget, error) {
	f.calls++
	f.payload = append([]byte(nil), p...)
	b, _ := auth.BearerFromContext(ctx)
	f.bearers = append(f.bearers, b)
	t := f.target
	return &t, f.err
}

type commandRig struct {
	api     *Api
	dec     *fakeDecider
	targets *fakeTargets
	ctx     context.Context
}

func newCommandRig(t *testing.T) *commandRig {
	t.Helper()
	api := newTestApi(t)
	require.NoError(t, api.RDB.Database.AutoMigrate(&AgentProposal{}, &AgentActuationFence{}, &rdb.AuditEvent{}))
	require.NoError(t, api.RDB.Database.Exec("CREATE UNIQUE INDEX agent_request_test ON agent_proposals(tenant_id,proposer_client,proposer_sub,request_id)").Error)
	require.NoError(t, api.RDB.Database.Exec("CREATE UNIQUE INDEX agent_effect_test ON commands(tenant_id,proposal_token)").Error)
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	d := &fakeDecider{act: actiongate.ActuationDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, Expiry: time.Hour}, exec: execApprove(1, "boss")}
	f := &fakeTargets{target: ProposalTarget{Exists: true, Incarnation: 42, Constrained: true, Found: true, Allowed: true, Impact: "NORMAL", DefinitionFingerprint: "definition-one", Anchors: []string{"customer"}}}
	api.AgentDecider = d
	api.ProposalTargets = f
	api.CommandProposalsEnabled = true
	api.KillFeed = NewKillFeed(0, nil)
	api.KillFeed.Beat(time.Now())
	return &commandRig{api: api, dec: d, targets: f, ctx: auth.WithBearer(core.WithTenant(context.Background(), "acme"), "agent-bearer")}
}
func commandReq(id string) CommandProposalRequest {
	return CommandProposalRequest{DeviceToken: "dev-1", Name: "reboot", RequestId: id}
}
func (r *commandRig) propose(t *testing.T, req CommandProposalRequest) *CommandProposalOutcome {
	t.Helper()
	out, err := r.api.ProposeCommand(r.ctx, rigAgent, req)
	require.NoError(t, err)
	require.NotNil(t, out)
	return out
}
func (r *commandRig) pending(t *testing.T) *AgentProposal {
	t.Helper()
	out := r.propose(t, commandReq("one"))
	require.Equal(t, ProposalStatusPending, out.Status)
	require.NotNil(t, out.Proposal)
	return out.Proposal
}
func (r *commandRig) stored(t *testing.T, p *AgentProposal) *AgentProposal {
	t.Helper()
	var out AgentProposal
	require.NoError(t, r.api.RDB.DB(r.ctx).Where("token = ?", p.Token).First(&out).Error)
	return &out
}
func (r *commandRig) commands(t *testing.T) []Command {
	t.Helper()
	var rows []Command
	require.NoError(t, r.api.RDB.DB(r.ctx).Find(&rows).Error)
	return rows
}
func (r *commandRig) approve(p *AgentProposal) (*AgentProposal, error) {
	a := approver("boss", time.Now())
	return r.api.ApproveAgentProposal(auth.WithBearer(auth.WithClaims(r.ctx, a.Claims), "human-bearer"), a, p.Token, p.Digest)
}

func TestCommandFlagDisabled(t *testing.T) {
	r := newCommandRig(t)
	r.api.CommandProposalsEnabled = false
	_, err := r.api.ProposeCommand(r.ctx, rigAgent, commandReq("one"))
	require.True(t, actiongate.IsCode(err, "COMMAND_PROPOSALS_DISABLED"))
	require.NotErrorIs(t, err, auth.ErrForbidden)
	_, err = r.api.ApproveAgentProposal(r.ctx, approver("boss", time.Now()), "missing", "wrong")
	require.True(t, actiongate.IsCode(err, "COMMAND_PROPOSALS_DISABLED"))
	require.Zero(t, r.targets.calls)
}
func TestCommandCanonicalIdentity(t *testing.T) {
	r := newCommandRig(t)
	now := time.Now().UTC()
	r.api.proposalNow = func() time.Time { return now }
	digest := func(payload *string, edit func(*CommandProposalRequest)) string {
		req := commandReq("dry")
		req.DryRun = true
		req.Payload = payload
		if edit != nil {
			edit(&req)
		}
		return r.propose(t, req).Digest
	}
	for _, ok := range []string{`{"a":-0.5,"b":-10,"c":0,"d":0.0,"e":12345678901234567890123}`} {
		require.NotEmpty(t, digest(&ok, nil), ok)
	}
	a, b := `{"b":2,"a":1.10}`, `{"a":1.10,"b":2}`
	require.Equal(t, digest(&a, nil), digest(&b, nil))
	c := `{"a":1.1,"b":2}`
	require.NotEqual(t, digest(&a, nil), digest(&c, nil))
	null := "null"
	require.NotEqual(t, digest(nil, nil), digest(&null, nil))
	require.NotEqual(t, digest(nil, nil), digest(nil, func(q *CommandProposalRequest) { q.DeviceToken = "dev-2" }))
	exp := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	require.NotEqual(t, digest(nil, nil), digest(nil, func(q *CommandProposalRequest) { q.ExpiresAt = &exp }))
	old := digest(nil, nil)
	r.targets.target.DefinitionFingerprint = "changed"
	require.NotEqual(t, old, digest(nil, nil))
	// Values Postgres jsonb would not store byte-for-byte (an exponent, a negative zero) or
	// at all (a NUL) are refused: stored, they would make a proposal no one can approve.
	for _, bad := range []string{`{"a":1,"a":2}`, `{"nested":[{"a":1,"a":2}]}`, `[]`, `1`, `{} {}`, `{"a":1e2}`, `{"a":[0.10E1]}`, `{"a":{"b":-0}}`, `{"a":-0.0}`, `{"a":"x\u0000y"}`, `{"a\u0000":1}`} {
		req := commandReq("bad")
		req.Payload = &bad
		_, err := r.api.ProposeCommand(r.ctx, rigAgent, req)
		require.Error(t, err, bad)
	}
}
func TestCommandIntentRetryBeforeSnapshot(t *testing.T) {
	r := newCommandRig(t)
	req := commandReq("same")
	why := "first"
	req.Rationale = &why
	first := r.propose(t, req)
	r.targets.target.DefinitionFingerprint = "new"
	r.targets.err = errors.New("unavailable")
	why2 := "second"
	req.Rationale = &why2
	again := r.propose(t, req)
	require.Equal(t, ProposalStatusExisting, again.Status)
	require.Equal(t, first.Proposal.Token, again.Proposal.Token)
	require.Equal(t, "first", *again.Proposal.Rationale)
	require.Equal(t, 1, r.targets.calls)
	req.DeviceToken = "dev-2"
	_, err := r.api.ProposeCommand(r.ctx, rigAgent, req)
	require.True(t, conflict.Is(err))
}
func TestCommandRefusalsStoredAndDryRun(t *testing.T) {
	for _, tc := range []struct {
		code   string
		change func(*commandRig)
	}{
		{"DEVICE_NOT_FOUND", func(r *commandRig) { r.targets.target.Exists = false }},
		{"OPEN_VOCABULARY_NOT_SUPPORTED", func(r *commandRig) { r.targets.target.Constrained = false }},
		{"COMMAND_NOT_IN_VOCABULARY", func(r *commandRig) { r.targets.target.Found = false }},
		{"PAYLOAD_REJECTED", func(r *commandRig) { r.targets.target.Allowed = false }},
		{"AUTO_NOT_AVAILABLE", func(r *commandRig) { r.dec.act.Verdict = actiongate.VerdictAuto }},
		{"GROUP_RESTRICTION_NOT_SUPPORTED", func(r *commandRig) { r.dec.act.GroupRestriction = []string{} }},
		{"DESTRUCTIVE_NEEDS_TWO_APPROVERS", func(r *commandRig) { r.targets.target.Impact = "DESTRUCTIVE" }},
		{"WINDOW_TOO_LATE", func(r *commandRig) { r.targets.target.ScheduleRefusal = &BatchDeviceRefusal{Code: "WINDOW_TOO_LATE"} }},
		{"IMPACT_UNKNOWN", func(r *commandRig) { r.targets.target.Impact = "" }},
	} {
		t.Run(tc.code, func(t *testing.T) {
			r := newCommandRig(t)
			tc.change(r)
			req := commandReq("refused")
			out := r.propose(t, req)
			require.Equal(t, ProposalStatusRefused, out.Status)
			require.Contains(t, out.Reasons, tc.code)
			require.Equal(t, "REFUSED", out.Proposal.State)
			require.Empty(t, r.commands(t))
			req.DryRun = true
			out = r.propose(t, req)
			require.Equal(t, ProposalStatusDryRun, out.Status)
			require.Nil(t, out.Proposal)
		})
	}
}
func TestCommandApprovalDigestAndCorruption(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	_, err := r.api.ApproveAgentProposal(r.ctx, approver("boss", time.Now()), p.Token, "wrong")
	require.True(t, actiongate.IsCode(err, actiongate.CodeDigestMismatch))
	require.Empty(t, r.dec.execReqs)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(p.Payload, &payload))
	payload["v"] = 2
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("payload", datatypes.JSON(raw)).Error)
	_, err = r.approve(p)
	require.Error(t, err)
	require.Empty(t, r.commands(t))
	require.Zero(t, r.stored(t, p).Revision)
}
func TestCommandApprovalParity(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	r.dec.exec = execApprove(2, "boss", "carol")
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "PROPOSED", got.State)
	require.Equal(t, 1, got.Revision)
	again, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, got.Revision, again.Revision)
	r.dec.exec = execApprove(2, "carol")
	got, err = r.api.ApproveAgentProposal(r.ctx, approver("carol", time.Now()), p.Token, p.Digest)
	require.NoError(t, err)
	require.Equal(t, "PROPOSED", got.State)
	require.Empty(t, r.commands(t))
	staleRig := newCommandRig(t)
	staleProposal := staleRig.pending(t)
	_, err = staleRig.api.ApproveAgentProposal(staleRig.ctx, approver("boss", time.Now().Add(-time.Hour)), staleProposal.Token, staleProposal.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeStepUpRequired))
	r.dec.exec = execApprove(2, rigAgent.Principal)
	r.dec.exec.AllowSelfConfirm = true
	_, err = r.api.ApproveAgentProposal(r.ctx, approver(rigAgent.Principal, time.Now()), p.Token, p.Digest)
	require.True(t, actiongate.IsCode(err, actiongate.CodeSelfApproval))
}

type visibleNudger struct {
	t     *testing.T
	r     *commandRig
	token string
	calls int
}

func (n *visibleNudger) NudgeDevice(tenant, device string) {
	n.calls++
	require.Equal(n.t, "acme", tenant)
	require.Equal(n.t, "dev-1", device)
	ctx, cancel := context.WithTimeout(n.r.ctx, 2*time.Second)
	defer cancel()
	var rows []Command
	require.NoError(n.t, n.r.api.RDB.DB(ctx).Where("token = ?", n.token).Find(&rows).Error)
	require.Len(n.t, rows, 1, "nudger must see committed command")
}
func TestCommandExecutionAtomicAndNudge(t *testing.T) {
	r := newCommandRig(t)
	raw := `{"z":1.10,"a":9007199254740993}`
	req := commandReq("one")
	req.Payload = &raw
	p := r.propose(t, req).Proposal
	n := &visibleNudger{t: t, r: r, token: p.EffectToken}
	r.api.Nudger = n
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "EXECUTED", got.State)
	require.Equal(t, p.EffectToken, *got.ResultRef)
	require.Equal(t, 1, n.calls)
	rows := r.commands(t)
	require.Len(t, rows, 1)
	c := rows[0]
	require.Equal(t, p.EffectToken, c.Token)
	require.Equal(t, p.Token, c.ProposalToken.String)
	require.Equal(t, "PROPOSAL", c.CauseKind.String)
	require.EqualValues(t, 42, c.DeviceIncarnation.Int64)
	require.Equal(t, `{"a":9007199254740993,"z":1.10}`, string(*c.Payload))
	require.True(t, c.ExpiresAt.Valid)
	require.Nil(t, c.Cause())
	require.Equal(t, p.Token, *c.CauseDetails().ProposalId)
	for _, q := range r.dec.execReqs {
		require.Equal(t, "reboot", q.Command)
		require.Equal(t, actiongate.KindCommandSend, q.Kind)
		require.NotEmpty(t, q.ApproverSessions)
	}
	require.Equal(t, []string{"agent-bearer", "human-bearer"}, r.targets.bearers)
	_, err = r.approve(p)
	require.Error(t, err)
	require.Equal(t, 1, n.calls)
}
func TestCommandDependencyDrift(t *testing.T) {
	for _, field := range []string{"incarnation", "definition", "impact", "missing", "allowed", "constrained", "found"} {
		t.Run(field, func(t *testing.T) {
			r := newCommandRig(t)
			p := r.pending(t)
			switch field {
			case "incarnation":
				r.targets.target.Incarnation++
			case "definition":
				r.targets.target.DefinitionFingerprint = "new"
			case "impact":
				r.targets.target.Impact = "DISRUPTIVE"
			case "missing":
				r.targets.target.Exists = false
			case "allowed":
				r.targets.target.Allowed = false
			case "constrained":
				r.targets.target.Constrained = false
			case "found":
				r.targets.target.Found = false
			}
			got, err := r.approve(p)
			require.NoError(t, err)
			require.Equal(t, "REFUSED", got.State)
			require.Contains(t, string(got.Reasons), "DEPENDENCY_CHANGED")
			require.Empty(t, r.commands(t))
		})
	}
}
func TestCommandCommitPointRollsBack(t *testing.T) {
	for _, mode := range []string{"refuse", "forbidden", "timeout", "auto", "groups"} {
		t.Run(mode, func(t *testing.T) {
			r := newCommandRig(t)
			p := r.pending(t)
			n := &visibleNudger{t: t, r: r, token: p.EffectToken}
			r.api.Nudger = n
			r.dec.second = func() (actiongate.ExecutionDecision, error) {
				d := r.dec.exec
				switch mode {
				case "refuse":
					d.Verdict = actiongate.VerdictRefuse
				case "auto":
					d.Verdict = actiongate.VerdictAuto
				case "groups":
					d.GroupRestriction = []string{}
				case "forbidden":
					return d, auth.ErrForbidden
				case "timeout":
					return d, context.DeadlineExceeded
				}
				return d, nil
			}
			got, err := r.approve(p)
			if mode == "forbidden" || mode == "timeout" {
				require.Error(t, err)
				require.Equal(t, "PROPOSED", r.stored(t, p).State)
			} else {
				require.NoError(t, err)
				require.Equal(t, "REFUSED", got.State)
			}
			require.Empty(t, r.commands(t))
			require.Zero(t, n.calls)
		})
	}
}
func TestCommandEffectTokenCollision(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	_, err := r.api.CreateCommand(r.ctx, &CommandCreateRequest{Token: p.EffectToken, DeviceToken: "foreign", Name: "other"})
	require.NoError(t, err)
	_, err = r.approve(p)
	require.True(t, actiongate.IsCode(err, "TOKEN_IN_USE"))
	require.Equal(t, "PROPOSED", r.stored(t, p).State)
	rows := r.commands(t)
	require.Len(t, rows, 1)
	require.Equal(t, "foreign", rows[0].DeviceToken)
}
func TestCommandExecutionClientBudget(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	now := time.Now().UTC() // SQLite compares stored times as text: write UTC, as production does
	for i := 0; i < 50; i++ {
		row := *p
		row.ID = 0
		row.Token = fmt.Sprintf("history-%d", i)
		row.RequestId = row.Token
		row.ProposerSub = fmt.Sprintf("different-%d", i)
		row.State = "EXECUTED"
		row.ExecutedAt = &now
		require.NoError(t, r.api.RDB.DB(r.ctx).Create(&row).Error)
	}
	_, err := r.approve(p)
	require.True(t, actiongate.IsCode(err, "EXECUTION_RATE_EXCEEDED"), "got %v", err)
	var gateErr *actiongate.Error
	require.ErrorAs(t, err, &gateErr)
	require.True(t, gateErr.Retryable)
	require.Equal(t, "PROPOSED", r.stored(t, p).State)
	require.Empty(t, r.commands(t))
}
func TestCommandCutoffAtCommitTail(t *testing.T) {
	r := newCommandRig(t)
	clock := time.Now().UTC().Truncate(time.Second)
	r.api.proposalNow = func() time.Time { return clock }
	req := commandReq("one")
	exp := clock.Add(2 * time.Minute).Format(time.RFC3339)
	req.ExpiresAt = &exp
	p := r.propose(t, req).Proposal
	r.dec.second = func() (actiongate.ExecutionDecision, error) {
		clock = clock.Add(3 * time.Minute)
		return r.dec.exec, nil
	}
	// Keep approval expiry independent so the command cutoff check is the guard.
	require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("expires_at", clock.Add(time.Hour)).Error)
	got, err := r.approve(p)
	require.NoError(t, err)
	require.Equal(t, "REFUSED", got.State)
	require.Contains(t, string(got.Reasons), "COMMAND_CUTOFF_PASSED")
	require.Empty(t, r.commands(t))
}

type forbiddenServiceSeams struct{ t *testing.T }

func (f forbiddenServiceSeams) ValidateEnqueue(context.Context, string, string, []byte) ([]string, uint64, error) {
	f.t.Fatal("agent path called service-token EnqueueValidator")
	return nil, 0, errors.New("forbidden")
}
func (f forbiddenServiceSeams) ScheduleDispatch(context.Context, []string, string, sql.NullTime) ([]DispatchHold, []BatchDeviceRefusal, error) {
	f.t.Fatal("agent path called service-token DispatchScheduler")
	return nil, nil, errors.New("forbidden")
}
func (f forbiddenServiceSeams) PullDevices(context.Context, []string) (map[string]bool, error) {
	f.t.Fatal("agent path called service-token PullChannels")
	return nil, errors.New("forbidden")
}
func TestCommandNeverUsesServiceTokenSeams(t *testing.T) {
	r := newCommandRig(t)
	f := forbiddenServiceSeams{t}
	r.api.EnqueueValidator = f
	r.api.DispatchScheduler = f
	r.api.PullChannels = f
	p := r.pending(t)
	_, err := r.approve(p)
	require.NoError(t, err)
}
func TestCommandRevisionRecheckedAfterDecision(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	// Simulate another intermediate approval after each pre-decision, before the lock.
	r.dec.beforeExec = func() {
		require.NoError(t, r.api.RDB.DB(r.ctx).Model(&AgentProposal{}).Where("token = ?", p.Token).Update("revision", gorm.Expr("revision + 1")).Error)
	}
	_, err := r.approve(p)
	require.True(t, actiongate.IsCode(err, "CONCURRENT_APPROVAL"))
	require.Empty(t, r.commands(t))
}

// A command already admitted for this proposal and since SOFT-DELETED is hidden by gorm's
// default scope; the admission must still see it and refuse, terminally, rather than run
// into the unique (tenant_id, proposal_token) index and report something retryable.
func TestCommandSoftDeletedAdmissionIsAlreadyAdmitted(t *testing.T) {
	r := newCommandRig(t)
	p := r.pending(t)
	prior := &Command{TokenReference: rdb.TokenReference{Token: "earlier-effect"}, DeviceToken: "dev-1", Name: "reboot", Status: CommandCancelled.String(), Outcome: sql.NullString{String: OutcomeNotDelivered.String(), Valid: true}, ProposalToken: sql.NullString{String: p.Token, Valid: true}, CauseKind: sql.NullString{String: CauseKindProposal, Valid: true}}
	require.NoError(t, r.api.RDB.DB(r.ctx).Create(prior).Error)
	require.NoError(t, r.api.RDB.DB(r.ctx).Delete(prior).Error)
	n := &visibleNudger{t: t, r: r, token: p.EffectToken}
	r.api.Nudger = n
	_, err := r.approve(p)
	require.True(t, actiongate.IsCode(err, "ALREADY_ADMITTED"), "got %v", err)
	var ae *actiongate.Error
	require.ErrorAs(t, err, &ae)
	require.False(t, ae.Retryable)
	require.Empty(t, r.commands(t))
	require.Zero(t, n.calls)
	var all int64
	require.NoError(t, r.api.RDB.DB(r.ctx).Unscoped().Model(&Command{}).Count(&all).Error)
	require.EqualValues(t, 1, all)
}
