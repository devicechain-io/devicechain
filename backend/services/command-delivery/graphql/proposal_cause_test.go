// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
	"testing"
)

type proposalCauseRoot struct{ command *CommandResolver }

func (r *proposalCauseRoot) Command() *CommandResolver { return r.command }

func TestProposalCauseVisibilityThroughResolver(t *testing.T) {
	for _, tc := range []struct {
		name    string
		claims  *auth.Claims
		visible bool
	}{
		{"human command reader", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"command:read"}}, true},
		{"human admin", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"*"}}, true},
		{"no command read", &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: []string{"device:read", "alarm:read", "location:read"}}, false},
		{"service", &auth.Claims{TokenType: auth.TokenTypeService, Authorities: []string{"*"}}, false},
		{"agent client", &auth.Claims{TokenType: auth.TokenTypeAccess, ClientId: "agent", Authorities: []string{"command:read"}}, false},
		{"agent scope", &auth.Claims{TokenType: auth.TokenTypeAccess, Scope: "read-only", Authorities: []string{"command:read"}}, false},
		{"scoped", &auth.Claims{TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{"customer:c"}, Authorities: []string{"command:read"}}, false},
		{"scoped to no anchors", &auth.Claims{TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{}, Authorities: []string{"command:read"}}, false},
		{"anonymous", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := model.Command{CauseKind: sql.NullString{String: "PROPOSAL", Valid: true}}
			require.NoError(t, json.Unmarshal([]byte(`{"ProposalToken":{"String":"p","Valid":true}}`), &c))
			schema, err := gql.ParseSchema(`type Query { command: Command } type Command { cause: CommandCause causeDetails: CommandCauseDetails proposal: AgentProposalReference } type CommandCause { kind: String! } type CommandCauseDetails { kind: String! proposalId: String detection: CommandCause } type AgentProposalReference { area: String! token: String! }`, &proposalCauseRoot{command: &CommandResolver{M: c}})
			require.NoError(t, err)
			ctx := context.Background()
			if tc.claims != nil {
				ctx = auth.WithClaims(ctx, tc.claims)
			}
			res := schema.Exec(ctx, `{ command { cause { kind } causeDetails { kind proposalId detection { kind } } proposal { area token } } }`, "", nil)
			require.Empty(t, res.Errors)
			want := `{"command":{"cause":null,"causeDetails":null,"proposal":null}}`
			if tc.visible {
				want = `{"command":{"cause":null,"causeDetails":{"kind":"PROPOSAL","proposalId":"p","detection":null},"proposal":{"area":"command-delivery","token":"p"}}}`
			}
			require.JSONEq(t, want, string(res.Data))
		})
	}
}

func TestPublicProposalCauseIsCauseInvalid(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	cause := alarmCause()
	cause["kind"] = "PROPOSAL"
	delete(cause, "alarmKey")
	got := decodeCreate(t, exec(t, ctx, createCommandMutation, causeVars("forged-proposal", cause)))
	require.NotNil(t, got.CreateCommand.Rejection)
	require.Equal(t, "CAUSE_INVALID", got.CreateCommand.Rejection.Code)
	require.Nil(t, got.CreateCommand.Command)
}
