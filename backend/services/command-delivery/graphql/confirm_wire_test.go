// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"strings"
	"testing"

	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// confirmMutation is the document the LwM2M transport sends, field and argument names
// included; lwm2m-ingest/downlink/claimer.go pins the same text on its side.
const confirmMutation = `mutation($token: String!, $dispatchNonce: String!) {
  confirmCommandDispatch(token: $token, dispatchNonce: $dispatchNonce)
}`

type confirmResult struct {
	ConfirmCommandDispatch *string `json:"confirmCommandDispatch"`
}

// TestConfirmCommandDispatchCrossesTheWire: an authorized confirm answers a NEW nonce that the
// row now carries, and a second confirm quoting the envelope's nonce (a redelivered copy of the
// same envelope) answers null and does not move the row again.
func TestConfirmCommandDispatchCrossesTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
		Token: "confirm-wire", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	n, claimed, err := api.MarkSent(ctx, created.ID)
	if err != nil || !claimed {
		t.Fatalf("fixture claim: claimed=%v err=%v", claimed, err)
	}

	var got confirmResult
	if err := json.Unmarshal(exec(t, ctx, confirmMutation,
		map[string]any{"token": "confirm-wire", "dispatchNonce": n}), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ConfirmCommandDispatch == nil || *got.ConfirmCommandDispatch == "" || *got.ConfirmCommandDispatch == n {
		t.Fatalf("confirmCommandDispatch answered %v for a SENT row on its own nonce; want a new nonce",
			got.ConfirmCommandDispatch)
	}
	n2 := *got.ConfirmCommandDispatch
	after, err := api.CommandsByToken(ctx, []string{"confirm-wire"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after[0].DispatchNonce.String != n2 || after[0].Status != model.CommandSent.String() {
		t.Fatalf("row = %s/%v, want SENT on the answered nonce %q", after[0].Status, after[0].DispatchNonce, n2)
	}

	// The redelivered copy of the same envelope loses: null, not an error, and the row stays on n2.
	if err := json.Unmarshal(exec(t, ctx, confirmMutation,
		map[string]any{"token": "confirm-wire", "dispatchNonce": n}), &got); err != nil {
		t.Fatalf("decode repeat: %v", err)
	}
	if got.ConfirmCommandDispatch != nil {
		t.Fatalf("a second confirm quoting the superseded nonce answered %q; the duplicate would actuate",
			*got.ConfirmCommandDispatch)
	}
	after, _ = api.CommandsByToken(ctx, []string{"confirm-wire"})
	if after[0].DispatchNonce.String != n2 {
		t.Fatalf("a lost confirm moved the row's nonce to %v", after[0].DispatchNonce)
	}
}

// TestConfirmCommandDispatchRequiresTheClaimAuthority: the confirm is gated on command:claim.
// A caller with command:write (what REACT's send-command sink mints) and a tenant access token
// holding command:claim are both refused, and the row does not move — the gate runs before
// the write, not after it.
func TestConfirmCommandDispatchRequiresTheClaimAuthority(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.Claims
	}{
		{"a service token holding command:write but not command:claim", &auth.Claims{
			Authorities: []string{string(auth.CommandRead), string(auth.CommandWrite), string(auth.CommandPark)},
			TokenType:   auth.TokenTypeService,
		}},
		{"a tenant access token holding command:claim", &auth.Claims{
			Authorities: []string{string(auth.CommandClaim)},
			TokenType:   auth.TokenTypeAccess,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, api := newWireTestCtx(t)
			created, err := api.CreateCommand(ctx, &model.CommandCreateRequest{
				Token: "confirm-gate", DeviceToken: "pump-1", Name: "reboot",
			})
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			n, claimed, err := api.MarkSent(ctx, created.ID)
			if err != nil || !claimed {
				t.Fatalf("fixture claim: claimed=%v err=%v", claimed, err)
			}

			schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
			res := schema.Exec(auth.WithClaims(ctx, tc.claims), confirmMutation, "",
				map[string]any{"token": "confirm-gate", "dispatchNonce": n})
			if len(res.Errors) == 0 {
				t.Fatal("confirmCommandDispatch must require command:claim")
			}
			if !strings.Contains(strings.ToLower(res.Errors[0].Message), "forbidden") {
				t.Fatalf("expected a forbidden error, got %q", res.Errors[0].Message)
			}
			after, err := api.CommandsByToken(ctx, []string{"confirm-gate"})
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if after[0].DispatchNonce.String != n {
				t.Fatalf("a refused confirm rotated the nonce to %v; the gate ran too late", after[0].DispatchNonce)
			}
		})
	}
}
