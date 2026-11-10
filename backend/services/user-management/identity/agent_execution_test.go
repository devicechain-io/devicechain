// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/agentpolicy"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// The proposal-bound decision an owning area asks when a person approves an agent's proposal,
// driven over the real store, the real session records and the real grant rules.

// execEnv is an agent env with a proposer whose agent holds an act:alarms grant, an approver role
// and the proposer's grant session.
type execEnv struct {
	*agentEnv
	role *iam.Role
	sid  auth.SessionID
}

func newExecEnv(t *testing.T) *execEnv {
	t.Helper()
	e := newAgentEnv(t, string(auth.AlarmWrite))
	e.publish(t, openPolicyJS)
	role := &iam.Role{Scope: iam.ScopeTenant, Token: "approvers", Authorities: []string{string(auth.AlarmWrite)}}
	require.NoError(t, e.store.CreateRole(context.Background(), role))
	x := &execEnv{agentEnv: e, role: role, sid: e.sidOf(t, agentPerson)}
	x.addApprover(t, "boss@example.invalid")
	return x
}

// addApprover adds an enabled member in the approver role, with an active second factor.
func (x *execEnv) addApprover(t *testing.T, email string) {
	t.Helper()
	require.NoError(t, x.store.CreateIdentity(context.Background(), &iam.Identity{
		Email: email, Enabled: true, PasswordHash: "x",
		Memberships: []iam.Membership{{TenantId: agentTenant, Enabled: true, TenantRoles: []iam.Role{*x.role}}},
	}))
	x.enrollFactor(t, email)
}

func personClaims(email string, authorities ...string) *auth.Claims {
	return &auth.Claims{Tenant: agentTenant, Username: email, Authorities: authorities, TokenType: auth.TokenTypeAccess}
}

func (x *execEnv) request(approvers ...string) ExecutionRequest {
	return ExecutionRequest{
		Kind: agentpolicy.KindAlarmClear, TargetCount: 1, ProposerPrincipal: agentPerson,
		ProposerClient: agentClient, ProposerSid: string(x.sid), Approvers: approvers,
	}
}

func (x *execEnv) decide(t *testing.T, req ExecutionRequest) ExecutionDecision {
	t.Helper()
	d, err := x.m.AgentExecutionDecision(context.Background(), personClaims("boss@example.invalid", string(auth.AlarmWrite)), req)
	require.NoError(t, err)
	return d
}

// Only a PERSON holding the kind's raw write may ask: an agent's token, a service token's shape
// with a client, and a person without the write are refused before anything is read.
func TestExecutionDecisionAnswersOnlyAPersonHoldingTheWrite(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	for name, c := range map[string]*auth.Claims{
		"agent":          {Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "act:alarms", Authorities: []string{string(auth.AlarmPropose)}, TokenType: auth.TokenTypeAccess},
		"agent with *":   {Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Authorities: []string{"*"}, TokenType: auth.TokenTypeAccess},
		"no write":       personClaims("boss@example.invalid", string(auth.AlarmRead)),
		"command writer": personClaims("boss@example.invalid", string(auth.CommandWrite)),
	} {
		_, err := x.m.AgentExecutionDecision(ctx, c, x.request("boss@example.invalid"))
		require.ErrorIs(t, err, auth.ErrForbidden, name)
	}
	d := x.decide(t, x.request("boss@example.invalid"))
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict, "control: a person holding alarm:write is answered (%v)", d.Reasons)
	require.Equal(t, 1, d.RequiredApprovals)
	require.Equal(t, []string{"boss@example.invalid"}, d.ValidApprovers)
	require.Equal(t, agentpolicy.DefaultStepUpMaxAge, d.StepUp.MaxAge)
	require.True(t, d.AllowSelfConfirm)
}

// A queued proposal dies with its proposer's standing: losing the write, the sign-in, the
// membership, or the client's admission all refuse it, each with its own reason.
func TestRevokedProposerProposalRefusedAtExecution(t *testing.T) {
	ctx := context.Background()

	t.Run("write revoked", func(t *testing.T) {
		x := newExecEnv(t)
		id, err := x.store.IdentityByEmail(ctx, agentPerson)
		require.NoError(t, err)
		role := id.Memberships[0].TenantRoles[0]
		role.Authorities = []string{string(auth.CommandWrite)}
		require.NoError(t, x.m.db.Database.Save(&role).Error)
		x.assertStandingRefusal(t, x.request("boss@example.invalid"), ReasonProposerLostAuthority)
	})
	t.Run("sign-in ended", func(t *testing.T) {
		x := newExecEnv(t)
		// Signing out ends the sign-in record by name, which is what a logout and "sign out
		// everywhere" both do.
		require.NoError(t, x.m.refreshKV.Delete(sessionKey(x.sid)))
		x.assertStandingRefusal(t, x.request("boss@example.invalid"), ReasonProposerSessionEnded)
	})
	t.Run("no sid", func(t *testing.T) {
		x := newExecEnv(t)
		req := x.request("boss@example.invalid")
		req.ProposerSid = ""
		x.assertStandingRefusal(t, req, ReasonProposerSessionEnded)
	})
	t.Run("membership disabled", func(t *testing.T) {
		x := newExecEnv(t)
		id, err := x.store.IdentityByEmail(ctx, agentPerson)
		require.NoError(t, err)
		require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.Membership{}).
			Where("id = ?", id.Memberships[0].ID).Update("enabled", false).Error)
		x.assertStandingRefusal(t, x.request("boss@example.invalid"), ReasonProposerNotMember)
	})
	t.Run("client disabled", func(t *testing.T) {
		x := newExecEnv(t)
		require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&iam.OAuthClient{}).
			Where("client_id = ?", agentClient).Update("enabled", false).Error)
		x.assertStandingRefusal(t, x.request("boss@example.invalid"), ReasonProposerClientRefused)
	})
	t.Run("client delisted", func(t *testing.T) {
		x := newExecEnv(t)
		x.publish(t, `{"actuation":"enabled","clients":["someone-else"],"approvers":{"roles":["approvers"]}}`)
		x.assertStandingRefusal(t, x.request("boss@example.invalid"), agentpolicy.ReasonClientNotAllowed)
	})
}

// The kill switches refuse an approved, unexecuted proposal: the tenant's (K1) and the operator's
// (K3) and the instance flag, each read now.
func TestKillSwitchRefusesApprovedUnexecuted(t *testing.T) {
	ctx := context.Background()
	x := newExecEnv(t)
	boss := personClaims("boss@example.invalid", string(auth.AlarmWrite))
	_, err := x.m.SetTenantAgentActuation(ctx, &auth.Claims{Tenant: agentTenant, Username: "admin@example.invalid",
		Authorities: []string{string(auth.AgentAdmin)}, TokenType: auth.TokenTypeAccess}, false)
	require.NoError(t, err)
	d, err := x.m.AgentExecutionDecision(ctx, boss, x.request("boss@example.invalid"))
	require.NoError(t, err)
	require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
	require.Equal(t, []string{agentpolicy.ReasonActuationOff}, d.Reasons)
	view, err := x.m.AgentPolicy(ctx, agentTenant)
	require.NoError(t, err)
	require.True(t, view.Head.TenantActuationOff)
	require.Equal(t, 1, view.Published.Version)
	doc, err := agentpolicy.ParseStored([]byte(view.Published.Document))
	require.NoError(t, err)
	require.True(t, doc.ActuationOn(), "the flag must refuse an enabled document")

	y := newExecEnv(t)
	_, err = y.store.SetAgentPolicySuspended(ctx, agentTenant, true, "operator")
	require.NoError(t, err)
	d = y.decide(t, y.request("boss@example.invalid"))
	require.Equal(t, []string{agentpolicy.ReasonOperatorSuspended}, d.Reasons)

	z := newExecEnv(t)
	z.m.SetAgentActuation(false)
	d = z.decide(t, z.request("boss@example.invalid"))
	require.Equal(t, []string{agentpolicy.ReasonInstanceOff}, d.Reasons)
}

// EVERY named approver is re-validated, not only the caller: one who has since lost the role, the
// membership or the second factor is not returned as valid.
func TestRevokedFirstApproverNoLongerCounts(t *testing.T) {
	ctx := context.Background()
	x := newExecEnv(t)
	x.addApprover(t, "second@example.invalid")
	x.addApprover(t, "third@example.invalid")
	d := x.decide(t, x.request("boss@example.invalid", "second@example.invalid", "third@example.invalid", "stranger@example.invalid"))
	require.Equal(t, []string{"boss@example.invalid", "second@example.invalid", "third@example.invalid"}, d.ValidApprovers)

	// second loses the approver role; third loses the second factor.
	other := &iam.Role{Scope: iam.ScopeTenant, Token: "operators", Authorities: []string{string(auth.AlarmWrite)}}
	require.NoError(t, x.store.CreateRole(ctx, other))
	id, err := x.store.IdentityByEmail(ctx, "second@example.invalid")
	require.NoError(t, err)
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Model(&id.Memberships[0]).Association("TenantRoles").Replace([]iam.Role{*other}))
	tid, err := x.store.IdentityByEmail(ctx, "third@example.invalid")
	require.NoError(t, err)
	require.NoError(t, x.m.db.Database.WithContext(core.WithSystemContext(ctx)).Where("identity_id = ?", tid.ID).Delete(&iam.MfaFactor{}).Error)

	d = x.decide(t, x.request("boss@example.invalid", "second@example.invalid", "third@example.invalid"))
	require.Equal(t, []string{"boss@example.invalid"}, d.ValidApprovers)
}

// The step-up and self-confirmation rules come from the CURRENT policy.
func TestExecutionDecisionCarriesThePolicysApprovalRules(t *testing.T) {
	x := newExecEnv(t)
	x.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"],"stepUpMaxAge":"5m","allowSelfConfirm":false},"stepUp":{"acceptRecoveryCodes":true}}`)
	d := x.decide(t, x.request("boss@example.invalid"))
	require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
	require.Equal(t, 5*time.Minute, d.StepUp.MaxAge)
	require.True(t, d.StepUp.AcceptRecoveryCodes)
	require.False(t, d.AllowSelfConfirm)
	require.Equal(t, 2, d.PolicyVersion)
}

// The figures beside the actuation verdict come from the same policy version, and the rate can
// only be what the policy sets.
func TestActuationDetailComesFromThePolicy(t *testing.T) {
	x := newExecEnv(t)
	ctx := context.Background()
	agent := &auth.Claims{Tenant: agentTenant, Username: agentPerson, ClientId: agentClient, Scope: "act:alarms",
		Authorities: []string{string(auth.AlarmPropose)}, TokenType: auth.TokenTypeAccess}
	det, err := x.m.AgentActuationDetail(ctx, agent, agentpolicy.KindAlarmAck, 1)
	require.NoError(t, err)
	require.Equal(t, agentpolicy.DefaultExpiry, det.Expiry)
	require.Zero(t, det.ActionsPerHour)
	require.False(t, det.ClientAttested, "a public client is a label, not an attested identity")

	x.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers"]},"expiry":{"default":"30m"},"tools":{"alarms":{"ratePerHour":5}}}`)
	_, err = x.m.AgentActuationDetail(ctx, agent, agentpolicy.KindAlarmAck, 1)
	require.Error(t, err, "figures from a newer policy than the verdict are refused")
	det, err = x.m.AgentActuationDetail(ctx, agent, agentpolicy.KindAlarmAck, 2)
	require.NoError(t, err)
	require.Equal(t, 30*time.Minute, det.Expiry)
	require.Equal(t, 5, det.ActionsPerHour)

	_, err = x.m.AgentActuationDetail(ctx, personClaims(agentPerson, "*"), agentpolicy.KindAlarmAck, 2)
	require.ErrorIs(t, err, auth.ErrForbidden)
}

// Detailed standing belongs only to the proposer or an agent administrator.
func (x *execEnv) assertStandingRefusal(t *testing.T, req ExecutionRequest, reason string) {
	t.Helper()
	for _, c := range []*auth.Claims{
		personClaims("boss@example.invalid", string(auth.AlarmWrite)),
		personClaims("admin@example.invalid", string(auth.AgentAdmin), string(auth.AlarmWrite)),
		personClaims(agentPerson, string(auth.AlarmWrite)),
		personClaims("root@example.invalid", "*"),
	} {
		d, err := x.m.AgentExecutionDecision(context.Background(), c, req)
		require.NoError(t, err)
		require.Equal(t, agentpolicy.VerdictRefuse, d.Verdict)
		want := reason
		if c.Username != req.ProposerPrincipal && !c.HasAuthority(auth.AgentAdmin) && reason != agentpolicy.ReasonClientNotAllowed {
			want = ReasonProposerStanding
		}
		require.Equal(t, []string{want}, d.Reasons, c.Username)
	}
}

// A best-effort KV deletion cannot preserve standing after the database epoch rotates.
func TestProposerSessionEpoch(t *testing.T) {
	for _, name := range []string{"password reset ends standing", "MFA reset ends standing", "legacy record without epoch"} {
		t.Run(name, func(t *testing.T) {
			x := newExecEnv(t)
			ctx := context.Background()
			if name == "MFA reset ends standing" {
				// This reset removes a sealed seed, so use the real secret store as production does.
				require.NoError(t, secrets.NewSecretStoreSchema().Migrate(x.m.db.Database.WithContext(core.WithSystemContext(ctx))))
				store, err := secrets.NewFromConfig(ctx, rootKeyConfig(t), x.m.db.Database)
				require.NoError(t, err)
				x.m.secrets = store
				id, err := x.store.IdentityByEmail(ctx, agentPerson)
				require.NoError(t, err)
				enrollFactor(t, x.store, x.m.secrets, id.ID)
				x.sid = x.sidOf(t, agentPerson)
			}
			require.Equal(t, agentpolicy.VerdictApprove, x.decide(t, x.request("boss@example.invalid")).Verdict)
			id, err := x.store.IdentityByEmail(ctx, agentPerson)
			require.NoError(t, err)
			switch name {
			case "password reset ends standing":
				require.NoError(t, x.store.SetPasswordHash(ctx, id, "new-hash"))
			case "MFA reset ends standing":
				require.NoError(t, x.store.ResetMfa(ctx, x.m.secrets, id, ""))
			default:
				// The real record with ONLY its epoch removed, so nothing else about it refuses.
				entry, err := x.m.refreshKV.Get(sessionKey(x.sid))
				require.NoError(t, err)
				var rec map[string]any
				require.NoError(t, json.Unmarshal(entry.Value(), &rec))
				require.NotEmpty(t, rec["epoch"], "control: a sign-in records its epoch")
				delete(rec, "epoch")
				raw, err := json.Marshal(rec)
				require.NoError(t, err)
				_, err = x.m.refreshKV.Put(sessionKey(x.sid), raw)
				require.NoError(t, err)
			}
			_, err = x.m.refreshKV.Get(sessionKey(x.sid))
			require.NoError(t, err, "the record still exists; the epoch check must refuse it")
			x.assertStandingRefusal(t, x.request("boss@example.invalid"), ReasonProposerSessionEnded)
		})
	}
}

// Completion re-reads every approver's current role and write authority.
func TestApproverLosingRoleBetweenApprovalAndCompletion(t *testing.T) {
	for _, mode := range []string{"role", "write", "scope", "factor"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			x := newExecEnv(t)
			require.Equal(t, []string{"boss@example.invalid"}, x.decide(t, x.request("boss@example.invalid")).ValidApprovers)
			id, err := x.store.IdentityByEmail(ctx, "boss@example.invalid")
			require.NoError(t, err)
			db := x.m.db.Database.WithContext(core.WithSystemContext(ctx))
			switch mode {
			case "role":
				other := &iam.Role{Scope: iam.ScopeTenant, Token: "operators", Authorities: []string{string(auth.AlarmWrite)}}
				require.NoError(t, x.store.CreateRole(ctx, other))
				require.NoError(t, db.Model(&id.Memberships[0]).Association("TenantRoles").Replace([]iam.Role{*other}))
			case "write":
				role := id.Memberships[0].TenantRoles[0]
				role.Authorities = []string{string(auth.CommandWrite)}
				require.NoError(t, db.Save(&role).Error)
				// Give the second approver a separate role so restoring their write cannot restore boss's.
				x.role = &iam.Role{Scope: iam.ScopeTenant, Token: "other-approvers", Authorities: []string{string(auth.AlarmWrite)}}
				require.NoError(t, x.store.CreateRole(ctx, x.role))
				x.publish(t, `{"actuation":"enabled","clients":["agent-desktop"],"approvers":{"roles":["approvers","other-approvers"]}}`)
			case "scope":
				require.NoError(t, db.Model(&id.Memberships[0]).Update("scope", []byte(`["customer:one"]`)).Error)
			case "factor":
				require.NoError(t, db.Where("identity_id = ?", id.ID).Delete(&iam.MfaFactor{}).Error)
			}
			x.addApprover(t, "second@example.invalid")
			d := x.decide(t, x.request("boss@example.invalid", "second@example.invalid"))
			require.Equal(t, agentpolicy.VerdictApprove, d.Verdict)
			require.Equal(t, []string{"second@example.invalid"}, d.ValidApprovers)
		})
	}
}
