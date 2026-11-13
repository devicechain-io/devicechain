// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
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

// The approver's sign-in must still be one the tenant admits: a password sign-in stops counting
// once the tenant requires single sign-on, as it would stop minting tokens there.
func TestApproverSignInTheTenantNoLongerAdmitsIsNotCounted(t *testing.T) {
	x := newExecEnv(t)
	// An agent service account proposes, so the tenant's sign-in rule touches only the approver.
	req := tenantServiceAccountRequest(t, x)
	require.Contains(t, x.decide(t, req).ValidApprovers, approvalBoss)
	ctx := context.Background()
	tenant, err := x.store.TenantByToken(ctx, agentTenant)
	require.NoError(t, err)
	require.NoError(t, x.store.SetTenantSsoEnforced(ctx, tenant, true))
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
		"empty principal": {{Sid: uuid.NewString()}},
		"empty sid":       {{Principal: approvalBoss}},
		"duplicate":       {{Principal: approvalBoss, Sid: uuid.NewString()}, {Principal: approvalBoss, Sid: uuid.NewString()}},
		"not an approver": {{Principal: "other", Sid: uuid.NewString()}},
	} {
		t.Run(name, func(t *testing.T) {
			req := x.request(approvalBoss)
			req.ApproverSessions = sessions
			_, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
			require.Error(t, err)
		})
	}
	const malformed = "approver session principal or sid is malformed"
	for name, session := range map[string]ApproverSession{
		"long principal": {Principal: strings.Repeat("a", 257), Sid: uuid.NewString()},
		"long sid":       {Principal: approvalBoss, Sid: strings.Repeat("a", 65)},
		"non UUID":       {Principal: approvalBoss, Sid: "not-a-uuid"},
		"URN UUID":       {Principal: approvalBoss, Sid: "urn:uuid:" + uuid.NewString()},
		"undashed UUID":  {Principal: approvalBoss, Sid: strings.ReplaceAll(uuid.NewString(), "-", "")},
		"36 non-hex":     {Principal: approvalBoss, Sid: strings.Repeat("z", 36)},
	} {
		t.Run(name, func(t *testing.T) {
			req := x.request(session.Principal)
			req.ApproverSessions = []ApproverSession{session}
			original := x.m.refreshKV
			kv := &countingApproverKV{KeyValue: original}
			x.m.refreshKV = kv
			defer func() { x.m.refreshKV = original }()
			_, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
			require.ErrorContains(t, err, malformed)
			require.Zero(t, kv.gets, "a malformed session is refused before any sign-in is read")
		})
	}
	t.Run("maximum principal length", func(t *testing.T) {
		principal := strings.Repeat("a", 256)
		req := x.request(principal)
		req.ApproverSessions = []ApproverSession{{Principal: principal, Sid: uuid.NewString()}}
		_, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
		if err != nil {
			require.NotContains(t, err.Error(), malformed)
		}
	})
}

func TestProposerSessionIsValidatedBeforeReads(t *testing.T) {
	x := newExecEnv(t)
	for name, input := range map[string]struct {
		principal, sid, message string
	}{
		"long principal":                 {strings.Repeat("a", 257), uuid.NewString(), "maximum principal length is 256 bytes"},
		"long service-account principal": {auth.ServiceAccountSubject(strings.Repeat("a", 257)), "", "maximum principal length is 256 bytes"},
		"long sid":                       {agentPerson, strings.Repeat("a", 65), "canonical 36-character UUID"},
		"non UUID":                       {agentPerson, "not-a-uuid", "canonical 36-character UUID"},
		"URN UUID":                       {agentPerson, "urn:uuid:" + uuid.NewString(), "canonical 36-character UUID"},
		"braced UUID":                    {agentPerson, "{" + uuid.NewString() + "}", "canonical 36-character UUID"},
		"undashed UUID":                  {agentPerson, strings.ReplaceAll(uuid.NewString(), "-", ""), "canonical 36-character UUID"},
		"36 non-hex":                     {agentPerson, strings.Repeat("z", 36), "canonical 36-character UUID"},
		"service-account malformed sid":  {auth.ServiceAccountSubject("alarm-agent"), "not-a-uuid", "canonical 36-character UUID"},
	} {
		t.Run(name, func(t *testing.T) {
			req := x.request(approvalBoss)
			req.ProposerPrincipal, req.ProposerSid = input.principal, input.sid
			original := x.m.refreshKV
			kv := &countingApproverKV{KeyValue: original}
			x.m.refreshKV = kv
			defer func() { x.m.refreshKV = original }()
			d, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
			require.ErrorContains(t, err, "proposer session")
			require.ErrorContains(t, err, input.message)
			require.Empty(t, d.Verdict, "malformed input returns a validation error rather than a refusal")
			require.Zero(t, kv.gets, "a malformed proposer is rejected before any sign-in is read")
		})
	}
}

func TestProposerSessionValidationAllowsBoundaries(t *testing.T) {
	x := newExecEnv(t)
	t.Run("maximum principal length", func(t *testing.T) {
		req := x.request(approvalBoss)
		req.ProposerPrincipal = strings.Repeat("a", 256)
		original := x.m.refreshKV
		kv := &countingApproverKV{KeyValue: original}
		x.m.refreshKV = kv
		defer func() { x.m.refreshKV = original }()
		d, err := x.m.AgentExecutionDecision(context.Background(), personClaims(approvalBoss, string(auth.AlarmWrite)), req)
		require.NoError(t, err)
		require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict, "the unknown principal fails standing, not input validation")
		require.Equal(t, 2, kv.gets, "a 256-byte principal and canonical sid pass validation")
	})
	t.Run("service-account subject fits", func(t *testing.T) {
		token := strings.Repeat("a", core.MaxTokenLen)
		require.NoError(t, core.ValidateToken(token))
		require.Len(t, auth.ServiceAccountSubject(token), len(auth.ServiceAccountSubjectPrefix)+core.MaxTokenLen)
		require.LessOrEqual(t, len(auth.ServiceAccountSubject(token)), maxApproverPrincipalLen)
	})
	t.Run("service-account empty sid", func(t *testing.T) {
		req := tenantServiceAccountRequest(t, x)
		require.Empty(t, req.ProposerSid)
		d := x.decide(t, req)
		require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
		require.Contains(t, d.ValidApprovers, approvalBoss)
	})
}

type countingApproverKV struct {
	nats.KeyValue
	gets     int
	cancelAt int
	cancel   context.CancelFunc
}

func (kv *countingApproverKV) Get(key string) (nats.KeyValueEntry, error) {
	kv.gets++
	if kv.cancel != nil && kv.gets == kv.cancelAt {
		kv.cancel()
	}
	return kv.KeyValue.Get(key)
}

func TestApproverSessionReadsStopOnCancellation(t *testing.T) {
	x := newExecEnv(t)
	x.addApprover(t, "other@example.invalid")
	// The proposer's sign-in is read first, then each approver's: three reads in all.
	req := x.request(approvalBoss, "other@example.invalid")
	for name, cancelAt := range map[string]int{"before reads": 0, "during the proposer read": 1, "during the first approver read": 2} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := x.m.refreshKV
			kv := &countingApproverKV{KeyValue: original, cancel: cancel, cancelAt: cancelAt}
			x.m.refreshKV = kv
			defer func() { x.m.refreshKV = original }()
			if cancelAt == 0 {
				cancel()
			}
			_, err := x.m.AgentExecutionDecision(ctx, personClaims(approvalBoss, string(auth.AlarmWrite)), req)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, cancelAt, kv.gets)
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
