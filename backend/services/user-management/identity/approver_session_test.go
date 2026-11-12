// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const approvalBoss = "boss@example.invalid"

func assertApprovalLapsed(t *testing.T, x *execEnv, req ExecutionRequest) {
	t.Helper()
	d := x.decide(t, req)
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
	require.NotContains(t, d.ValidApprovers, approvalBoss)
}

func TestApproverLiveSessionCounts(t *testing.T) {
	x := newExecEnv(t)
	require.Contains(t, x.decide(t, x.request(approvalBoss)).ValidApprovers, approvalBoss)
}

// Use the same signed identity token that the real sign-out path accepts.
func endApproverSignIn(t *testing.T, x *execEnv, sid string) {
	t.Helper()
	id, err := x.store.IdentityByEmail(context.Background(), approvalBoss)
	require.NoError(t, err)
	token, err := x.m.issuer.IssueIdentity(approvalBoss, auth.SessionEpoch(id.SessionEpoch), auth.SessionID(sid), nil, nil, uuid.NewString())
	require.NoError(t, err)
	_, err = x.m.endSignIn(context.Background(), token.Token)
	require.NoError(t, err)
}

func TestApproverSignOutLapsesTheirApproval(t *testing.T) {
	x := newExecEnv(t)
	req := x.request(approvalBoss)
	require.Contains(t, x.decide(t, req).ValidApprovers, approvalBoss)
	endApproverSignIn(t, x, req.ApproverSessions[0].Sid)
	assertApprovalLapsed(t, x, req)
}

func TestApproverPasswordResetLapsesTheirApproval(t *testing.T) {
	x := newExecEnv(t)
	req := x.request(approvalBoss)
	id, err := x.store.IdentityByEmail(context.Background(), approvalBoss)
	require.NoError(t, err)
	require.NoError(t, x.store.SetPasswordHash(context.Background(), id, "new-hash"))
	_, err = x.m.refreshKV.Get(sessionKey(auth.SessionID(req.ApproverSessions[0].Sid)))
	require.NoError(t, err, "the KV record survives; the epoch must end its standing")
	assertApprovalLapsed(t, x, req)
}

func TestApproverDisabledThenReenabledDoesNotReviveApproval(t *testing.T) {
	x := newExecEnv(t)
	req := x.request(approvalBoss)
	ctx := context.Background()
	id, err := x.store.IdentityByEmail(ctx, approvalBoss)
	require.NoError(t, err)
	epoch := id.SessionEpoch
	require.NoError(t, x.store.SetIdentityEnabled(ctx, id, false))
	stored, err := x.store.IdentityByEmail(ctx, approvalBoss)
	require.NoError(t, err)
	require.NotEqual(t, epoch, stored.SessionEpoch)
	require.NoError(t, x.store.SetIdentityEnabled(ctx, id, true))
	assertApprovalLapsed(t, x, req)
}

func TestApproverWithoutSessionIsNotCounted(t *testing.T) {
	x := newExecEnv(t)
	req := x.request(approvalBoss)
	req.ApproverSessions = nil
	assertApprovalLapsed(t, x, req)
}

func TestApproverSessionOfAnotherIdentityIsNotCounted(t *testing.T) {
	x := newExecEnv(t)
	req := x.request(approvalBoss)
	req.ApproverSessions[0].Sid = string(x.sid)
	assertApprovalLapsed(t, x, req)
}

func changeApproverRole(t *testing.T, x *execEnv, roles []iam.Role) {
	t.Helper()
	ctx := context.Background()
	id, err := x.store.IdentityByEmail(ctx, approvalBoss)
	require.NoError(t, err)
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&id.Memberships[0]).Association("TenantRoles").Replace(roles))
}

func TestApproverRoleRevokedThenRegrantedAfterNewSignInDoesNotRevive(t *testing.T) {
	x := newExecEnv(t)
	// Keep another eligible person so removing boss's role does not refuse the whole policy.
	x.addApprover(t, "other@example.invalid")
	req := x.request(approvalBoss)
	require.Contains(t, x.decide(t, req).ValidApprovers, approvalBoss)
	changeApproverRole(t, x, nil)
	assertApprovalLapsed(t, x, req)
	endApproverSignIn(t, x, req.ApproverSessions[0].Sid)
	changeApproverRole(t, x, []iam.Role{*x.role})
	sid2 := x.sidOf(t, approvalBoss)
	assertApprovalLapsed(t, x, req)
	req.ApproverSessions[0].Sid = string(sid2)
	require.Contains(t, x.decide(t, req).ValidApprovers, approvalBoss)
}

func TestApproverRoleRevokedAndRegrantedWithinOneSessionRevives(t *testing.T) {
	x := newExecEnv(t)
	x.addApprover(t, "other@example.invalid")
	req := x.request(approvalBoss)
	changeApproverRole(t, x, nil)
	assertApprovalLapsed(t, x, req)
	changeApproverRole(t, x, []iam.Role{*x.role})
	// Session binding cannot see a revoke+regrant inside one sign-in; only a per-principal
	// authorization generation would, which was deliberately not built.
	require.Contains(t, x.decide(t, req).ValidApprovers, approvalBoss)
}

func TestApproverSessionsAreValidated(t *testing.T) {
	x := newExecEnv(t)
	many := make([]ApproverSession, MaxExecutionApprovers+1)
	for name, sessions := range map[string][]ApproverSession{
		"too many":        many,
		"empty principal": {{Sid: "sid"}},
		"empty sid":       {{Principal: approvalBoss}},
		"duplicate":       {{Principal: approvalBoss, Sid: "one"}, {Principal: approvalBoss, Sid: "two"}},
		"not an approver": {{Principal: "other", Sid: "sid"}},
	} {
		t.Run(name, func(t *testing.T) {
			req := x.request(approvalBoss)
			req.ApproverSessions = sessions
			_, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
			require.Error(t, err)
		})
	}
}

func TestTenantAccessTokenCarriesTheSignInSid(t *testing.T) {
	e := newSessionEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Parse(pair.AccessToken)
	require.NoError(t, err)
	refresh, err := e.validator.Parse(pair.RefreshToken)
	require.NoError(t, err)
	require.NotEmpty(t, refresh.SessionID)
	require.Equal(t, refresh.SessionID, access.SessionID)
}
