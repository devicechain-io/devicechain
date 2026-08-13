// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	gql "github.com/graph-gophers/graphql-go"
)

// This file exercises createCommand's result payload THROUGH THE SCHEMA — real
// GraphQL, real resolver, real database — because the thing under test is a WIRE
// contract: a machine caller (REACT's send-command sink) decides whether to retry
// from the `code` field, and nothing below the schema can tell you whether that field
// arrives.
//
// 🔴 The request goes in as a VARIABLE, never as a query literal, for the reason
// statuses_wire_test.go states at length: every real client — the console, the SDKs,
// dcctl, codegen, REACT — sends variables, and this library's variable path is the
// one that historically diverged from the spec by silently discarding input-object
// entries the schema does not define. A literal would exercise the single path no
// caller uses.

// stubEnqueueValidator answers the enqueue gate with a canned verdict or failure.
type stubEnqueueValidator struct{ err error }

func (s stubEnqueueValidator) ValidateEnqueue(context.Context, string, string, []byte) error {
	return s.err
}

const createCommandMutation = `mutation($request: CommandCreateRequest!) {
  createCommand(request: $request) {
    command { token status }
    rejection { code reason }
  }
}`

// createCommandResult is the decoded payload. Both arms are POINTERS so that "absent"
// is distinguishable from "present and empty" — a rejection decoded into a value type
// would read as a zero-code rejection on a successful enqueue.
type createCommandResult struct {
	CreateCommand struct {
		Command *struct {
			Token  string `json:"token"`
			Status string `json:"status"`
		} `json:"command"`
		Rejection *struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"rejection"`
	} `json:"createCommand"`
}

func enqueue(t *testing.T, ctx context.Context, token string) createCommandResult {
	t.Helper()
	data := exec(t, ctx, createCommandMutation, map[string]any{
		"request": map[string]any{
			"token":       token,
			"deviceToken": "device-1",
			"name":        "reboot",
		},
	})
	var out createCommandResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding createCommand failed: %v", err)
	}
	return out
}

// TestCreateCommandRelaysTheRejectionCodeOnTheWire is the guard on the machine-
// readable channel this change exists to create.
//
// The rejection must arrive as DATA — a payload with a stable code — and NOT as a
// GraphQL error. The distinction is the whole point: the response envelope this stack
// decodes carries only {data, errors[].message}, so a code sent any other way (an
// `extensions` block, a prefix on the message) is discarded before any caller sees it,
// and REACT is left retrying a device-deleted command until its poison cap gives up.
func TestCreateCommandRelaysTheRejectionCodeOnTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	// The gate's own verdict, code and all. command-delivery must RELAY it rather than
	// re-deriving a classification from the reason text.
	api.EnqueueValidator = stubEnqueueValidator{err: &model.EnqueueRejected{
		Code:   "DEVICE_NOT_FOUND",
		Reason: `device "device-1" does not exist`,
	}}

	out := enqueue(t, ctx, "wire-rejected")

	if out.CreateCommand.Rejection == nil {
		t.Fatal("a refused enqueue must come back as a REJECTION payload; " +
			"without one the caller cannot tell an invalid command from an outage")
	}
	if out.CreateCommand.Rejection.Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("the gate's code must be relayed verbatim, got %q", out.CreateCommand.Rejection.Code)
	}
	if !strings.Contains(out.CreateCommand.Rejection.Reason, `does not exist`) {
		t.Fatalf("the client-safe reason must survive the relay, got %q", out.CreateCommand.Rejection.Reason)
	}
	if out.CreateCommand.Command != nil {
		t.Fatalf("a rejection must carry NO command, got %+v", out.CreateCommand.Command)
	}
	if got, _ := api.CommandsByToken(ctx, []string{"wire-rejected"}); len(got) != 0 {
		t.Fatalf("a refused enqueue must persist nothing, found %+v", got)
	}
}

// TestCreateCommandCeilingRejectionCodeOnTheWire pins the code command-delivery
// produces ITSELF, on the path that has no remote gate in it at all.
//
// It is the one rejection a caller must NOT treat as permanent — the tenant is full
// now and drains as its fleet returns — so its code has to be distinguishable on the
// wire from the rejections that will never change.
func TestCreateCommandCeilingRejectionCodeOnTheWire(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "held-a", model.CommandHeld)
	seedCommand(t, ctx, api, "held-b", model.CommandHeld)
	api.DefaultHeldCommandCeiling = 2

	out := enqueue(t, ctx, "wire-over-ceiling")

	if out.CreateCommand.Rejection == nil {
		t.Fatal("an enqueue over the held-command ceiling must be refused as a rejection payload")
	}
	if out.CreateCommand.Rejection.Code != string(model.RejectHeldCeilingExceeded) {
		t.Fatalf("expected %s on the wire, got %q",
			model.RejectHeldCeilingExceeded, out.CreateCommand.Rejection.Code)
	}
}

// TestCreateCommandSuccessArmOnTheWire is the counterweight. A rejection channel is
// only worth having while a valid command still comes back as a command — a payload
// that returned a rejection for everything would pass every assertion above.
func TestCreateCommandSuccessArmOnTheWire(t *testing.T) {
	ctx, _ := newWireTestCtx(t)

	out := enqueue(t, ctx, "wire-accepted")

	if out.CreateCommand.Command == nil {
		t.Fatal("a valid enqueue must come back as a COMMAND")
	}
	if out.CreateCommand.Command.Token != "wire-accepted" {
		t.Fatalf("wrong command returned: %+v", out.CreateCommand.Command)
	}
	if out.CreateCommand.Command.Status != model.CommandQueued.String() {
		t.Fatalf("a fresh command must be QUEUED, got %s", out.CreateCommand.Command.Status)
	}
	if out.CreateCommand.Rejection != nil {
		t.Fatalf("a successful enqueue must carry NO rejection, got %+v", out.CreateCommand.Rejection)
	}

	// A replay of the same token returns the ORIGINAL through the same success arm —
	// idempotency is not a rejection, and a client that saw one here would treat its own
	// safe retry as a failed command.
	replay := enqueue(t, ctx, "wire-accepted")
	if replay.CreateCommand.Rejection != nil || replay.CreateCommand.Command == nil {
		t.Fatalf("an idempotent replay must return the command, not a rejection: %+v", replay.CreateCommand)
	}
}

// TestCreateCommandAvailabilityFailureStaysAGraphQLError is the OTHER half of the
// contract, and the half that is easy to lose.
//
// A gate that could not be REACHED decided nothing. It must stay a GraphQL error:
// turning it into a rejection would assert something about the caller's command that
// nobody checked (sending them to fix a payload that was fine), and the detail behind
// it names in-cluster topology a tenant client has no business learning.
func TestCreateCommandAvailabilityFailureStaysAGraphQLError(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	api.EnqueueValidator = stubEnqueueValidator{
		err: errUnreachable{"dial tcp 10.42.0.7:8080: connect: connection refused"},
	}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, createCommandMutation, "", map[string]any{
		"request": map[string]any{
			"token": "wire-unavailable", "deviceToken": "device-1", "name": "reboot",
		},
	})
	if len(res.Errors) == 0 {
		t.Fatal("an unreachable enqueue gate must be a GraphQL ERROR, not a rejection; " +
			"a rejection claims the command is invalid when nothing checked it")
	}
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "10.42.0.7") || strings.Contains(e.Message, "connection refused") {
			t.Fatalf("the failure detail must not reach the tenant client (in-cluster topology): %q", e.Message)
		}
	}
	if got, _ := api.CommandsByToken(ctx, []string{"wire-unavailable"}); len(got) != 0 {
		t.Fatalf("an undecidable enqueue must fail CLOSED and persist nothing, found %+v", got)
	}
}

// errUnreachable is a plain (untyped-as-rejection) failure — what the validator
// returns when it could not reach device-management at all.
type errUnreachable struct{ msg string }

func (e errUnreachable) Error() string { return e.msg }
