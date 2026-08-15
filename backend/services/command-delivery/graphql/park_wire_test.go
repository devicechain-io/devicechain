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

const parkMutation = `mutation($token: String!, $dispatchNonce: String!) {
  parkCommand(token: $token, dispatchNonce: $dispatchNonce)
}`

// parkResult decodes the mutation's single boolean field.
type parkResult struct {
	ParkCommand bool `json:"parkCommand"`
}

// TestParkCommandRequiresItsOwnAuthority is the gate test, and the ROW ASSERTION is the
// test — the error is not.
//
// Parking is the only way anything outside this service moves a row BACKWARDS out of SENT,
// which is what makes it worth its own authority rather than a fold into command:claim. A
// gate applied after the model call would let an unauthorized caller pull an in-flight
// command back into the dispatchable set and only then be refused the answer: the damage
// done, the refusal cosmetic. Asserting only that an error came back cannot tell those two
// designs apart.
func TestParkCommandRequiresItsOwnAuthority(t *testing.T) {
	ctx, api := newWireTestCtx(t)

	authorized := withServiceAuthorities(ctx, auth.CommandWrite, auth.CommandRead, auth.CommandPark)
	created, err := api.CreateCommand(authorized, &model.CommandCreateRequest{
		Token: "park-gate", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	nonce, claimed, err := api.MarkSent(authorized, created.ID)
	if err != nil || !claimed {
		t.Fatalf("fixture claim: claimed=%v err=%v", claimed, err)
	}

	// 🔴 command:claim is deliberately NOT enough. It is the authority a transport already
	// holds, so reusing it was the tempting shortcut; this pins that the shortcut was not
	// taken. command:write is included too — the tenant-facing authority must never reach a
	// system-tier hand-back.
	unauthorized := withServiceAuthorities(ctx, auth.CommandWrite, auth.CommandRead, auth.CommandClaim)
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(unauthorized, parkMutation, "",
		map[string]any{"token": "park-gate", "dispatchNonce": nonce})
	if len(res.Errors) == 0 {
		t.Fatal("parkCommand must require command:park; command:claim and command:write must not do")
	}
	if !strings.Contains(strings.ToLower(res.Errors[0].Message), "forbidden") {
		t.Fatalf("expected a forbidden error, got %q", res.Errors[0].Message)
	}

	// AND THE ROW DID NOT MOVE. If the gate ran after the model call, every assertion above
	// would still hold while the command had already been dragged out of SENT.
	after, err := api.CommandsByToken(authorized, []string{"park-gate"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after[0].Status != model.CommandSent.String() {
		t.Fatalf("an unauthorized park moved the row to %s; the gate ran too late to matter",
			after[0].Status)
	}
}

// TestParkCommandCrossesTheWireAndMovesTheRow is the counterweight: the gate is only correct
// while an AUTHORIZED park still works, end to end through the schema.
//
// It goes through gql.Exec rather than calling the resolver, because the wire is where this
// contract actually breaks. The forked graphql-go rejects an input field the schema does not
// define when it arrives through a VARIABLE, which is how every real client sends one — so a
// misnamed argument fails loudly here rather than silently parking nothing and reporting
// success.
func TestParkCommandCrossesTheWireAndMovesTheRow(t *testing.T) {
	ctx, api := newWireTestCtx(t)

	authorized := withServiceAuthorities(ctx, auth.CommandWrite, auth.CommandRead, auth.CommandPark)
	created, err := api.CreateCommand(authorized, &model.CommandCreateRequest{
		Token: "park-wire", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	nonce, claimed, err := api.MarkSent(authorized, created.ID)
	if err != nil || !claimed {
		t.Fatalf("fixture claim: claimed=%v err=%v", claimed, err)
	}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(authorized, parkMutation, "",
		map[string]any{"token": "park-wire", "dispatchNonce": nonce})
	if len(res.Errors) > 0 {
		t.Fatalf("authorized park failed: %v", res.Errors)
	}
	var got parkResult
	if err := json.Unmarshal(res.Data, &got); err != nil {
		t.Fatalf("decoding parkCommand failed: %v", err)
	}
	if !got.ParkCommand {
		t.Fatal("parkCommand reported it moved nothing for a SENT row on its own nonce")
	}

	after, err := api.CommandsByToken(authorized, []string{"park-wire"})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after[0].Status != model.CommandParked.String() {
		t.Fatalf("status = %s, want PARKED", after[0].Status)
	}

	// 🔴 A SECOND park on the SAME nonce must report false rather than erroring. The row is
	// no longer SENT, so it names a dispatch that has ended — which is exactly the shape of a
	// JetStream redelivery arriving after the first park landed. A transport reading that as
	// failure would retry until its delivery budget ran out.
	res = schema.Exec(authorized, parkMutation, "",
		map[string]any{"token": "park-wire", "dispatchNonce": nonce})
	if len(res.Errors) > 0 {
		t.Fatalf("a repeated park must be a settled no-op, not an error: %v", res.Errors)
	}
	if err := json.Unmarshal(res.Data, &got); err != nil {
		t.Fatalf("decoding the repeated parkCommand failed: %v", err)
	}
	if got.ParkCommand {
		t.Fatal("a repeated park reported it moved the row a second time")
	}
}
