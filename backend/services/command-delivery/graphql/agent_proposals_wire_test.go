// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-command-delivery/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

var proposalOperations = map[string]string{
	"propose": `mutation { proposeCommand(input:{deviceToken:"device",name:"reboot",requestId:"one"}) { status } }`,
	"approve": `mutation { approveAgentProposal(token:"proposal",digest:"digest") { token } }`,
	"reject":  `mutation { rejectAgentProposal(token:"proposal") { token } }`,
	"read":    `{ agentProposal(token:"proposal") { token } }`,
	"list":    `{ agentProposals(criteria:{pageNumber:1,pageSize:10}) { results { token } } }`,
}

func TestAgentProposalSurfaceRefusesServiceTokens(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	ctx := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{"*"}})
	for name, document := range proposalOperations {
		t.Run(name, func(t *testing.T) {
			res := schema.Exec(ctx, document, "", nil)
			require.Len(t, res.Errors, 1)
			require.NotNil(t, res.Errors[0].ResolverError, "operation must exist and refuse at its authorization door: %v", res.Errors)
			require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrForbidden))
		})
	}
}

func proposalClaims(client string, authorities ...string) *auth.Claims {
	return &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A", Username: "alice", ClientId: client, Authorities: authorities}
}

func TestProposalAuthorizationBeforeModel(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	for _, tc := range []struct {
		name, op, code string
		claims         *auth.Claims
	}{
		{"console cannot propose", "propose", "AGENT_ONLY", proposalClaims("", "command:write")},
		{"raw write is not propose", "propose", "", proposalClaims("agent", "command:write")},
		{"agent cannot approve", "approve", "APPROVER_IS_AN_AGENT", proposalClaims("agent", "command:write")},
		{"read cannot approve", "approve", "", proposalClaims("", "command:read")},
		{"agent cannot reject", "reject", "APPROVER_IS_AN_AGENT", proposalClaims("agent", "command:write")},
		{"read cannot reject", "reject", "", proposalClaims("", "command:read")},
		{"rule agent cannot read", "read", "", proposalClaims("agent", "rule:propose")},
		{"rule agent cannot list", "list", "", proposalClaims("agent", "rule:propose")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := schema.Exec(auth.WithClaims(context.Background(), tc.claims), proposalOperations[tc.op], "", nil)
			require.Len(t, res.Errors, 1)
			require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrForbidden), "%v", res.Errors)
			if tc.code != "" {
				require.Equal(t, tc.code, res.Errors[0].Extensions["code"])
			}
		})
	}
}

func TestAgentProposalViewerFiltersPrincipalAndClient(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&model.AgentProposal{}))
	for _, row := range []struct{ token, client, principal string }{{"own", "agent-a", "alice"}, {"other-client", "agent-b", "alice"}, {"other-person", "agent-a", "bob"}} {
		p := model.AgentProposal{Record: actiongate.Record{Token: row.token, Kind: "COMMAND_SEND", State: "PROPOSED", Payload: []byte(`{"intent":{"deviceToken":"device","command":"reboot"},"effective":{"cutoff":"2026-11-21T12:00:00Z"}}`), Dependencies: []byte("{}"), TargetSet: []byte(`["device"]`), Approvals: []byte(`[]`), Reasons: []byte(`[]`), ProposerClient: row.client, ProposerSub: row.principal, ExpiresAt: time.Now().Add(time.Hour)}}
		require.NoError(t, api.RDB.DB(ctx).Create(&p).Error)
	}
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	agent := auth.WithClaims(ctx, proposalClaims("agent-a", "command:propose"))
	res := schema.Exec(agent, proposalOperations["list"], "", nil)
	require.Empty(t, res.Errors)
	require.JSONEq(t, `{"agentProposals":{"results":[{"token":"own"}]}}`, string(res.Data))
	for _, token := range []string{"other-client", "other-person"} {
		res = schema.Exec(agent, `query($token:String!){agentProposal(token:$token){token}}`, "", map[string]any{"token": token})
		require.NotEmpty(t, res.Errors, "agent must not read %s", token)
	}
	res = schema.Exec(auth.WithClaims(ctx, proposalClaims("", "command:read")), proposalOperations["list"], "", nil)
	require.Empty(t, res.Errors)
	var data struct{ AgentProposals struct{ Results []any } }
	require.NoError(t, json.Unmarshal(res.Data, &data))
	require.Len(t, data.AgentProposals.Results, 3)
	claims := proposalClaims("", "command:read")
	claims.ScopeAnchors = []string{"customer:c"}
	for _, op := range []string{"read", "list"} {
		res = schema.Exec(auth.WithClaims(ctx, claims), proposalOperations[op], "", nil)
		require.NotEmpty(t, res.Errors)
	}
}

func TestProposalViewerRefusesScopedHuman(t *testing.T) {
	claims := proposalClaims("", "command:read")
	claims.ScopeAnchors = []string{"customer:c"}
	_, err := proposalViewer(auth.WithClaims(context.Background(), claims))
	require.Error(t, err)
}

type wireProposalTarget struct{ err error }

func (r wireProposalTarget) ReadProposalTarget(context.Context, string, string, []byte, time.Time) (*model.ProposalTarget, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &model.ProposalTarget{Exists: true, Incarnation: 42, Constrained: true, Found: true, Allowed: true, Impact: "NORMAL", DefinitionFingerprint: "definition", Anchors: []string{"customer:c"}}, nil
}

func TestProposalCodedErrorsReachWire(t *testing.T) {
	for _, code := range []string{"ALREADY_ADMITTED", "EXECUTION_RATE_EXCEEDED"} {
		t.Run(code, func(t *testing.T) {
			ctx, api := newWireTestCtx(t)
			require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&model.AgentProposal{}, &model.AgentActuationFence{}, &rdb.AuditEvent{}))
			require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
			api.CommandProposalsEnabled = true
			api.AgentDecider = wireProposalDecider{}
			api.ProposalTargets = wireProposalTarget{}
			api.KillFeed = model.NewKillFeed(0, nil)
			api.KillFeed.Beat(time.Now())
			out, err := api.ProposeCommand(ctx, actiongate.Proposer{Tenant: "A", Principal: "alice", Client: "agent"}, model.CommandProposalRequest{DeviceToken: "device", Name: "reboot", RequestId: "one"})
			require.NoError(t, err)
			require.NotNil(t, out.Proposal)
			p := out.Proposal
			now := time.Now().UTC() // SQLite compares stored times as text: write UTC, as production does
			if code == "ALREADY_ADMITTED" {
				command := model.Command{TokenReference: rdb.TokenReference{Token: "previously-admitted"}, DeviceToken: "device", Name: "reboot", Status: "QUEUED", ProposalToken: sql.NullString{String: p.Token, Valid: true}}
				require.NoError(t, api.RDB.DB(ctx).Create(&command).Error)
				require.NoError(t, api.RDB.DB(ctx).Delete(&command).Error)
			} else {
				for i := 0; i < model.ExecutionsPerHourPerClient; i++ {
					row := model.AgentProposal{Record: actiongate.Record{Token: fmt.Sprintf("spent-%d", i), RequestId: fmt.Sprintf("spent-%d", i), Kind: "COMMAND_SEND", State: "EXECUTED", Payload: []byte("{}"), TargetSet: []byte("[]"), Dependencies: []byte("{}"), Approvals: []byte("[]"), Reasons: []byte("[]"), ProposerClient: "agent", ProposerSub: "another-person", ExecutedAt: &now, ExpiresAt: now}}
					require.NoError(t, api.RDB.DB(ctx).Create(&row).Error)
				}
			}
			claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A", Username: "boss", SessionID: auth.SessionID("boss-session"), Authorities: []string{"command:write"}, Amr: auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now)}
			res := gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(auth.WithBearer(auth.WithClaims(ctx, claims), "boss-bearer"), "mutation($t:String!,$d:String!){approveAgentProposal(token:$t,digest:$d){token}}", "", map[string]any{"t": p.Token, "d": p.Digest})
			require.Len(t, res.Errors, 1, "%v", res.Errors)
			require.Equal(t, code, res.Errors[0].Extensions["code"])
			require.Equal(t, code == "EXECUTION_RATE_EXCEEDED", res.Errors[0].Extensions["retryable"])
		})
	}
}

type wireProposalDecider struct{}

func (wireProposalDecider) Actuation(context.Context, actiongate.ActuationRequest) (actiongate.ActuationDecision, error) {
	return actiongate.ActuationDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, Expiry: time.Hour}, nil
}
func (wireProposalDecider) Execution(context.Context, actiongate.ExecutionRequest) (actiongate.ExecutionDecision, error) {
	return actiongate.ExecutionDecision{Verdict: actiongate.VerdictApprove, RequiredApprovals: 1, ValidApprovers: []string{"boss"}, ActuationGeneration: 3, StepUp: auth.StepUp{MaxAge: 15 * time.Minute}}, nil
}

func TestPublicCreateCommandCannotStampProposalCause(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	res := gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(ctx, createCommandMutation, "", causeVars("forged-proposal", func() map[string]any { c := alarmCause(); c["kind"] = "PROPOSAL"; return c }()))
	require.Empty(t, res.Errors)
	out := decodeCreate(t, res.Data)
	require.NotNil(t, out.CreateCommand.Rejection)
	require.Equal(t, "CAUSE_INVALID", out.CreateCommand.Rejection.Code)
}

type wireKillReader struct{}

func (wireKillReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	<-ctx.Done()
	return messaging.Message{}, ctx.Err()
}
func (wireKillReader) HandleResponse(error)                            {}
func (wireKillReader) Backlog(context.Context) (uint64, uint64, error) { return 0, 0, nil }

func TestKillBindFailureLeavesApprovalFencedUntilRecovery(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	require.NoError(t, api.RDB.DB(ctx).AutoMigrate(&model.AgentProposal{}, &model.AgentActuationFence{}, &rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(api.RDB.Database))
	api.CommandProposalsEnabled = true
	api.AgentDecider = wireProposalDecider{}
	api.ProposalTargets = wireProposalTarget{}
	api.KillFeed = model.NewKillFeed(0, nil)
	var allow atomic.Bool
	var attempts atomic.Int32
	consumer := processor.NewAgentKillConsumer(&core.Microservice{FunctionalArea: "command-delivery"}, nil, api, api.KillFeed, nil)
	consumer.BindReader = func() (messaging.MessageReader, error) {
		attempts.Add(1)
		if !allow.Load() {
			return nil, errors.New("old callout refused subscription")
		}
		return wireKillReader{}, nil
	}
	require.NoError(t, consumer.Initialize(context.Background()))
	require.NoError(t, consumer.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, consumer.Stop(context.Background())) })
	require.Eventually(t, func() bool { return attempts.Load() > 0 }, time.Second, 10*time.Millisecond)
	out, err := api.ProposeCommand(ctx, actiongate.Proposer{Tenant: "A", Principal: "alice", Client: "agent"}, model.CommandProposalRequest{DeviceToken: "device", Name: "reboot", RequestId: "fenced"})
	require.NoError(t, err)
	now := time.Now()
	claims := &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "A", Username: "boss", SessionID: auth.SessionID("boss-session"), Authorities: []string{"command:write"}, Amr: auth.AmrPasswordOTP(), MfaTime: jwt.NewNumericDate(now)}
	res := gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(auth.WithBearer(auth.WithClaims(ctx, claims), "boss-bearer"), "mutation($t:String!,$d:String!){approveAgentProposal(token:$t,digest:$d){token}}", "", map[string]any{"t": out.Proposal.Token, "d": out.Digest})
	require.Len(t, res.Errors, 1)
	require.Equal(t, "FENCE_UNAVAILABLE", res.Errors[0].Extensions["code"])
	var commands int64
	require.NoError(t, api.RDB.DB(ctx).Model(&model.Command{}).Count(&commands).Error)
	require.Zero(t, commands)
	allow.Store(true)
	require.Eventually(t, func() bool { return api.KillFeed.Fresh(api.KillFeed.Now()) }, 4*time.Second, 10*time.Millisecond)
}

func TestProposalEnqueueRefusalKeepsStableCode(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	api.CommandProposalsEnabled = true
	api.AgentDecider = wireProposalDecider{}
	api.ProposalTargets = wireProposalTarget{err: &model.EnqueueRejected{Code: "TENANT_UNDELIVERED_LIMIT", Reason: "tenant command capacity exhausted"}}
	res := gql.MustParseSchema(SchemaContent, &SchemaResolver{}).Exec(auth.WithClaims(ctx, proposalClaims("agent", "command:propose")), "mutation{proposeCommand(input:{deviceToken:\"device\",name:\"reboot\",requestId:\"one\",dryRun:true}){status}}", "", nil)
	require.Len(t, res.Errors, 1)
	require.Equal(t, "TENANT_UNDELIVERED_LIMIT", res.Errors[0].Extensions["code"])
}
