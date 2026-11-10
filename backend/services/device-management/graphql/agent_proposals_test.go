// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// The resolver doors of the agent-proposal ledger: who reaches which operation.

type approvingDecider struct{}

func (approvingDecider) Actuation(context.Context, actiongate.ActuationRequest) (actiongate.ActuationDecision, error) {
	return actiongate.ActuationDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, Expiry: time.Hour}, nil
}

func (approvingDecider) Execution(context.Context, actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	return actiongate.ExecutionDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1,
		ValidApprovers: []string{"boss@example.invalid"}, StepUp: auth.StepUp{MaxAge: 15 * time.Minute}, AllowSelfConfirm: true}, nil
}

// agentToken is what an OAuth grant of act:alarms mints for a person who holds alarm:write.
func agentToken() *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "ana@example.invalid",
		ClientId: "agent-desktop", Scope: "read-only act:alarms", SessionID: "sid-1",
		Authorities: []string{string(auth.AlarmRead), string(auth.AlarmPropose)}}
}

func personToken() *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "boss@example.invalid",
		Authorities: []string{string(auth.AlarmRead), string(auth.AlarmWrite)}, Amr: auth.AmrPasswordOTP(),
		MfaTime: jwt.NewNumericDate(time.Now())}
}

func proposalCtx(t *testing.T) (context.Context, *model.Api) {
	t.Helper()
	ctx, api := alarmAckTestCtx(t)
	require.NoError(t, api.RDB.Database.AutoMigrate(&model.AgentProposal{}))
	api.AgentDecider = approvingDecider{}
	return ctx, api
}

// 🔴 THE HEADLINE: a stolen agent token, sent straight to the API past any adapter, cannot change an
// alarm. It holds alarm:propose and never alarm:write, so the existing mutations refuse it; the only
// door it opens is a proposal, which changes nothing.
func TestStolenAgentTokenCannotCallAcknowledgeAlarmDirectly(t *testing.T) {
	ctx, api := proposalCtx(t)
	agent := auth.WithClaims(ctx, agentToken())
	r := &SchemaResolver{}

	_, err := r.AcknowledgeAlarm(agent, struct{ Token string }{Token: "alarm-01"})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.AcknowledgeAlarms(agent, struct{ Tokens []string }{Tokens: []string{"alarm-01", "alarm-02"}})
	require.ErrorIs(t, err, auth.ErrForbidden)
	_, err = r.ClearAlarm(agent, struct{ Token string }{Token: "alarm-01"})
	require.ErrorIs(t, err, auth.ErrForbidden)
	require.EqualValues(t, 3, unackedCount(t, ctx, api), "no alarm changed")

	out, err := r.ProposeAlarmAction(agent, struct{ Input AgentAlarmProposalInput }{Input: AgentAlarmProposalInput{
		Action: "ACK", AlarmTokens: []string{"alarm-01"}, RequestId: "r1"}})
	require.NoError(t, err, "control: the agent's one door is a proposal")
	require.Equal(t, model.ProposalStatusPending, out.Status())
	require.EqualValues(t, 3, unackedCount(t, ctx, api), "a proposal changes no alarm")
}

// A person proposes nothing (they act through the ordinary mutations), and an agent decides
// nothing: neither approve nor reject accepts an agent-origin token, and a service token reaches
// neither door.
func TestProposeAndDecideAreDisjointDoors(t *testing.T) {
	ctx, _ := proposalCtx(t)
	r := &SchemaResolver{}
	person := auth.WithClaims(ctx, personToken())
	agent := auth.WithClaims(ctx, agentToken())
	svc := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeService, Tenant: "acme", Username: "svc",
		Authorities: []string{string(auth.AlarmPropose), string(auth.AlarmWrite)}})
	input := struct{ Input AgentAlarmProposalInput }{Input: AgentAlarmProposalInput{Action: "CLEAR", AlarmTokens: []string{"alarm-01"}, RequestId: "r1"}}

	_, err := r.ProposeAlarmAction(person, input)
	require.True(t, actiongate.IsCode(err, actiongate.CodeAgentOnly), "got %v", err)
	_, err = r.ProposeAlarmAction(svc, input)
	require.True(t, actiongate.IsCode(err, actiongate.CodeServiceTokenRefused), "got %v", err)

	out, err := r.ProposeAlarmAction(agent, input)
	require.NoError(t, err)
	token, digest := out.Proposal().Token(), out.Digest()

	_, err = r.ApproveAgentProposal(agent, struct{ Token, Digest string }{Token: token, Digest: digest})
	require.True(t, actiongate.IsCode(err, actiongate.CodeApproverIsAgent), "got %v", err)
	_, err = r.RejectAgentProposal(agent, struct {
		Token string
		Note  *string
	}{Token: token})
	require.True(t, actiongate.IsCode(err, actiongate.CodeApproverIsAgent), "got %v", err)
	_, err = r.ApproveAgentProposal(svc, struct{ Token, Digest string }{Token: token, Digest: digest})
	require.True(t, actiongate.IsCode(err, actiongate.CodeServiceTokenRefused), "got %v", err)

	p, err := r.ApproveAgentProposal(person, struct{ Token, Digest string }{Token: token, Digest: digest})
	require.NoError(t, err)
	require.Equal(t, string(actiongate.StateExecuted), p.State())

}

// A person with alarm:read sees the tenant's proposals; an agent sees only its own client's; a
// token with neither sees nothing, and neither does a scoped member.
func TestProposalReadsAreScopedToTheCaller(t *testing.T) {
	ctx, _ := proposalCtx(t)
	r := &SchemaResolver{}
	agent := auth.WithClaims(ctx, agentToken())
	input := struct{ Input AgentAlarmProposalInput }{Input: AgentAlarmProposalInput{Action: "ACK", AlarmTokens: []string{"alarm-01"}, RequestId: "r1"}}
	out, err := r.ProposeAlarmAction(agent, input)
	require.NoError(t, err)
	token := out.Proposal().Token()

	_, err = r.AgentProposal(auth.WithClaims(ctx, personToken()), struct{ Token string }{Token: token})
	require.NoError(t, err)
	_, err = r.AgentProposal(agent, struct{ Token string }{Token: token})
	require.NoError(t, err)

	otherAgent := agentToken()
	otherAgent.ClientId = "another-agent"
	_, err = r.AgentProposal(auth.WithClaims(ctx, otherAgent), struct{ Token string }{Token: token})
	require.Error(t, err, "another client does not see this client's proposal")

	nobody := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme", Username: "v@example.invalid", Authorities: []string{string(auth.DeviceRead)}}
	_, err = r.AgentProposal(auth.WithClaims(ctx, nobody), struct{ Token string }{Token: token})
	require.ErrorIs(t, err, auth.ErrForbidden)
	svc := &auth.Claims{TokenType: auth.TokenTypeService, Tenant: "acme", Username: "svc",
		Authorities: []string{string(auth.AlarmRead)}}
	_, err = r.AgentProposal(auth.WithClaims(ctx, svc), struct{ Token string }{Token: token})
	require.ErrorIs(t, err, auth.ErrForbidden)
	scoped := personToken()
	scoped.ScopeAnchors = []string{"customer:c1"}
	_, err = r.AgentProposal(auth.WithClaims(ctx, scoped), struct{ Token string }{Token: token})
	require.ErrorIs(t, err, auth.ErrForbidden)
}

// A row persisted before the reasons were coarsened is read back coarse.
func TestProposalReasonsAreCoarseOnReadback(t *testing.T) {
	var m model.AgentProposal
	m.Reasons = []byte(`["PROPOSER_LOST_AUTHORITY","OTHER","PROPOSER_SCOPED"]`)
	r := &AgentProposalResolver{M: m}
	require.Equal(t, []string{actiongate.ReasonProposerNotInStanding, "OTHER"}, r.Reasons())
}
