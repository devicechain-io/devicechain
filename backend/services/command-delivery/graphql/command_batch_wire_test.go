// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// This file exercises createCommandBatch THROUGH THE SCHEMA, for the reason
// create_command_wire_test.go states: the thing under test is a WIRE contract, and
// nothing below the schema can tell you whether a field arrives.
//
// The request goes in as a VARIABLE, never a query literal — every real client sends
// variables, and this library's variable path is the one that historically diverged from
// the spec by silently discarding input-object entries the schema does not define.

const createBatchMutation = `mutation($request: CommandBatchCreateRequest!) {
  createCommandBatch(request: $request) {
    batch {
      token name targetKind groupToken groupVersion allowPartial resolved accepted
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
    rejection {
      code reason resolved
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
  }
}`

type wireRefusal struct {
	DeviceToken string `json:"deviceToken"`
	Code        string `json:"code"`
	Reason      string `json:"reason"`
}

type wireRefusalCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// createBatchResult decodes the payload. Both arms are POINTERS so "absent" stays
// distinguishable from "present and empty" — a rejection decoded into a value type would
// read as a zero-code rejection on a batch that succeeded.
type createBatchResult struct {
	CreateCommandBatch struct {
		Batch *struct {
			Token         string             `json:"token"`
			Name          string             `json:"name"`
			TargetKind    string             `json:"targetKind"`
			GroupToken    *string            `json:"groupToken"`
			GroupVersion  *int32             `json:"groupVersion"`
			AllowPartial  bool               `json:"allowPartial"`
			Resolved      int                `json:"resolved"`
			Accepted      int                `json:"accepted"`
			Refusals      []wireRefusal      `json:"refusals"`
			RefusalCounts []wireRefusalCount `json:"refusalCounts"`
		} `json:"batch"`
		Rejection *struct {
			Code          string             `json:"code"`
			Reason        string             `json:"reason"`
			Resolved      *int               `json:"resolved"`
			Refusals      []wireRefusal      `json:"refusals"`
			RefusalCounts []wireRefusalCount `json:"refusalCounts"`
		} `json:"rejection"`
	} `json:"createCommandBatch"`
}

// stubBatchValidator refuses the device tokens it is told to and allows the rest.
type stubBatchValidator struct {
	refuse map[string]model.RejectionCode
	err    error
}

func (s stubBatchValidator) ValidateEnqueueBatch(_ context.Context, deviceTokens []string,
	_ string, _ []byte) ([]model.BatchDeviceRefusal, error) {
	if s.err != nil {
		return nil, s.err
	}
	refusals := make([]model.BatchDeviceRefusal, 0)
	for _, token := range deviceTokens {
		if code, no := s.refuse[token]; no {
			refusals = append(refusals, model.BatchDeviceRefusal{
				DeviceToken: token, Code: code, Reason: "refused by test",
			})
		}
	}
	return refusals, nil
}

// stubGroupResolver answers a group walk in one page.
type stubGroupResolver struct {
	members  []string
	rejected *model.GroupTargetPage
}

func (s stubGroupResolver) ResolveGroupTargets(_ context.Context, _ string, _ *int32,
	afterCursor string, _ int) (*model.GroupTargetPage, error) {
	if s.rejected != nil {
		return s.rejected, nil
	}
	if afterCursor != "" {
		return &model.GroupTargetPage{}, nil
	}
	return &model.GroupTargetPage{DeviceTokens: s.members}, nil
}

func createBatch(t *testing.T, ctx context.Context, request map[string]any) createBatchResult {
	t.Helper()
	data := exec(t, ctx, createBatchMutation, map[string]any{"request": request})
	var out createBatchResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding createCommandBatch failed: %v", err)
	}
	return out
}

// listRequest builds a device-list batch request.
//
// The token list is []any, not []string, because these go in as VARIABLES and the
// library's variable path takes JSON-shaped values — the same shape a real client's
// decoded request body has. A []string reaches the packer as an opaque Go value and
// fails to coerce.
func listRequest(token string, devices ...string) map[string]any {
	tokens := make([]any, 0, len(devices))
	for _, device := range devices {
		tokens = append(tokens, device)
	}
	return map[string]any{
		"token": token, "name": "reboot", "allowPartial": false,
		"deviceTokens": tokens,
	}
}

// TestBatchCreatedOnTheWire is the baseline: the happy path renders every field the
// record carries, so the tests below can assert on DIFFERENCES from it.
func TestBatchCreatedOnTheWire(t *testing.T) {
	ctx, _ := newWireTestCtx(t)

	out := createBatch(t, ctx, listRequest("wire-batch", "pump-a", "pump-b"))

	if out.CreateCommandBatch.Batch == nil {
		t.Fatalf("a good batch must come back as `batch`, got rejection %+v",
			out.CreateCommandBatch.Rejection)
	}
	batch := out.CreateCommandBatch.Batch
	if batch.Token != "wire-batch" || batch.Name != "reboot" {
		t.Fatalf("the batch identity did not survive the wire: %+v", batch)
	}
	if batch.TargetKind != string(model.BatchTargetDeviceList) {
		t.Fatalf("expected a DEVICE_LIST batch, got %q", batch.TargetKind)
	}
	if batch.Resolved != 2 || batch.Accepted != 2 {
		t.Fatalf("expected 2 resolved and 2 accepted, got %d and %d", batch.Resolved, batch.Accepted)
	}
	if batch.GroupToken != nil || batch.GroupVersion != nil {
		t.Fatalf("a device-list batch carries no group, got %+v / %+v",
			batch.GroupToken, batch.GroupVersion)
	}
	if len(batch.Refusals) != 0 || len(batch.RefusalCounts) != 0 {
		t.Fatalf("nothing was refused, so both refusal fields must be empty: %+v", batch)
	}
	if out.CreateCommandBatch.Rejection != nil {
		t.Fatalf("a created batch must carry NO rejection, got %+v", out.CreateCommandBatch.Rejection)
	}
}

// TestRequestShapedRefusalArrivesAsARejection covers the *EnqueueRejected arm.
func TestRequestShapedRefusalArrivesAsARejection(t *testing.T) {
	ctx, _ := newWireTestCtx(t)

	// Neither a device list nor a group: refused before anything is resolved.
	out := createBatch(t, ctx, map[string]any{
		"token": "wire-ambiguous", "name": "reboot", "allowPartial": false,
	})

	rejection := out.CreateCommandBatch.Rejection
	if rejection == nil {
		t.Fatal("an ambiguous target must come back as a rejection payload, not an error")
	}
	if rejection.Code != string(model.RejectBatchTargetAmbiguous) {
		t.Fatalf("expected %s, got %q", model.RejectBatchTargetAmbiguous, rejection.Code)
	}
	// 🔴 The distinction this field exists for. A refusal decided before any target was
	// established must report NULL, not 0 — zero is a real answer meaning "the group
	// resolved to no devices", and an operator would act on it.
	if rejection.Resolved != nil {
		t.Fatalf("a refusal that never resolved a target must report resolved=null, got %d",
			*rejection.Resolved)
	}
	if out.CreateCommandBatch.Batch != nil {
		t.Fatalf("a rejection must carry no batch, got %+v", out.CreateCommandBatch.Batch)
	}
}

// TestDeviceShapedRefusalArrivesAsARejectionWithItsDevices is THE test this slice
// exists to justify.
//
// 🔴 THE FAILURE IT GUARDS IS A GraphQL LAYER THAT errors.As ONLY *EnqueueRejected. That
// implementation is the obvious one — it is what the single-command sibling does — and it
// would turn the two most common outcomes of a real fleet write (a heterogeneous fleet
// under the default allowPartial=false, and a tenant at its ceiling) into opaque server
// errors. The tenant would be told the platform is broken at the exact moment it is
// working correctly, and the device list naming what to fix would be discarded.
func TestDeviceShapedRefusalArrivesAsARejectionWithItsDevices(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	api.BatchValidator = stubBatchValidator{refuse: map[string]model.RejectionCode{
		"pump-b": "COMMAND_NOT_IN_VOCABULARY",
	}}

	out := createBatch(t, ctx, listRequest("wire-partial", "pump-a", "pump-b"))

	rejection := out.CreateCommandBatch.Rejection
	if rejection == nil {
		t.Fatal("a partially-refused batch must come back as a REJECTION payload; if this " +
			"is nil the resolver matched only *EnqueueRejected and the refusal became a " +
			"sanitized server error")
	}
	if rejection.Code != string(model.RejectBatchPartialRefused) {
		t.Fatalf("expected %s, got %q", model.RejectBatchPartialRefused, rejection.Code)
	}
	if rejection.Resolved == nil || *rejection.Resolved != 2 {
		t.Fatalf("the rejection must say how many devices were resolved, got %v", rejection.Resolved)
	}
	// The refusal list is why *BatchRejected is a distinct type at all. Without it the
	// operator has to bisect a fleet by hand to find the offending device.
	if len(rejection.Refusals) != 1 {
		t.Fatalf("expected exactly 1 named device, got %+v", rejection.Refusals)
	}
	if rejection.Refusals[0].DeviceToken != "pump-b" {
		t.Fatalf("the wrong device was named: %+v", rejection.Refusals[0])
	}
	if rejection.Refusals[0].Code != "COMMAND_NOT_IN_VOCABULARY" {
		t.Fatalf("the owner's code must be relayed verbatim, got %q", rejection.Refusals[0].Code)
	}
	if len(rejection.RefusalCounts) != 1 || rejection.RefusalCounts[0].Count != 1 {
		t.Fatalf("the per-code totals must travel with the sample, got %+v", rejection.RefusalCounts)
	}
}

// TestCeilingRefusalArrivesAsARejection covers the other *BatchRejected code — the one
// that is TEMPORARY, and so must be distinguishable on the wire from refusals that will
// never change.
func TestCeilingRefusalArrivesAsARejection(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	seedCommand(t, ctx, api, "held-a", model.CommandHeld)
	api.DefaultHeldCommandCeiling = 2

	out := createBatch(t, ctx, listRequest("wire-ceiling", "pump-a", "pump-b", "pump-c"))

	rejection := out.CreateCommandBatch.Rejection
	if rejection == nil {
		t.Fatal("a batch over the ceiling must come back as a rejection payload")
	}
	if rejection.Code != string(model.RejectHeldCeilingExceeded) {
		t.Fatalf("expected %s, got %q", model.RejectHeldCeilingExceeded, rejection.Code)
	}
	if rejection.Resolved == nil || *rejection.Resolved != 3 {
		t.Fatalf("expected resolved=3, got %v", rejection.Resolved)
	}
}

// TestPartialBatchRecordsItsRefusals covers the path where the RECORD is the only
// carrier of the refusals — there is no rejection, because the batch succeeded.
//
// 🔑 Without decoded refusal columns, a partially-admitted batch answers accepted < resolved
// with no explanation anywhere, which defeats the record's entire purpose.
func TestPartialBatchRecordsItsRefusals(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	api.BatchValidator = stubBatchValidator{refuse: map[string]model.RejectionCode{
		"pump-b": "DEVICE_NOT_FOUND",
	}}

	request := listRequest("wire-allow-partial", "pump-a", "pump-b")
	request["allowPartial"] = true
	out := createBatch(t, ctx, request)

	batch := out.CreateCommandBatch.Batch
	if batch == nil {
		t.Fatalf("allowPartial must let the batch through, got rejection %+v",
			out.CreateCommandBatch.Rejection)
	}
	if batch.Resolved != 2 || batch.Accepted != 1 {
		t.Fatalf("expected 2 resolved and 1 accepted, got %d and %d", batch.Resolved, batch.Accepted)
	}
	if len(batch.Refusals) != 1 || batch.Refusals[0].DeviceToken != "pump-b" {
		t.Fatalf("the record must name the device it skipped — nothing else will — got %+v",
			batch.Refusals)
	}
	// resolved = accepted + sum(counts) is the invariant that makes the record
	// self-auditing. Assert it on the WIRE, since that is where a caller checks it.
	total := 0
	for _, count := range batch.RefusalCounts {
		total += count.Count
	}
	if batch.Accepted+total != batch.Resolved {
		t.Fatalf("the record's arithmetic does not close: accepted %d + refused %d != resolved %d",
			batch.Accepted, total, batch.Resolved)
	}
}

// TestGroupTargetRequiresDeviceRead is the authority test for the confused deputy.
//
// 🔴 RESOLVING A GROUP IS A READ THIS SERVICE PERFORMS UNDER ITS OWN IDENTITY, which is
// minted with device:read. The answer then flows back to the caller — the refusal list
// names device tokens, and resolved/accepted disclose the group's size. So a caller
// holding command:write and NOT device:read could otherwise enumerate an entity group's
// membership by firing a batch at it, using our authority to answer a question it may not
// ask. REACT's send-command sink mints command:write alone, so such a token is real.
//
// Looping createCommand cannot do this: the loop requires the caller to already know
// every device token. A group target is the one path that hands tokens BACK.
func TestGroupTargetRequiresDeviceRead(t *testing.T) {
	ctx, api := newWireTestCtx(t)
	// A resolver that WOULD answer, so a pass here means the gate refused rather than
	// the group merely being unresolvable.
	api.GroupTargetResolver = stubGroupResolver{members: []string{"pump-a", "pump-b"}}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, createBatchMutation, "", map[string]any{
		"request": map[string]any{
			"token": "wire-group-denied", "name": "reboot", "allowPartial": false,
			"groupToken": "pumps-region-3",
		},
	})
	if len(res.Errors) == 0 {
		t.Fatal("a caller without device:read must be REFUSED a group-targeted batch; " +
			"it resolves group membership under this service's identity and hands the " +
			"result back")
	}
	if !strings.Contains(strings.ToLower(res.Errors[0].Message), "forbidden") {
		t.Fatalf("expected a forbidden error, got %q", res.Errors[0].Message)
	}

	// The negative control. The SAME request with device:read added must get through —
	// otherwise the test above would pass against a mutation that refuses everything.
	allowed := withServiceAuthorities(ctx, auth.CommandRead, auth.CommandWrite, auth.DeviceRead)
	out := createBatch(t, allowed, map[string]any{
		"token": "wire-group-allowed", "name": "reboot", "allowPartial": false,
		"groupToken": "pumps-region-3",
	})
	if out.CreateCommandBatch.Batch == nil {
		t.Fatalf("with device:read the same batch must succeed, got rejection %+v",
			out.CreateCommandBatch.Rejection)
	}
	if out.CreateCommandBatch.Batch.Resolved != 2 {
		t.Fatalf("expected the group's 2 members, got %d", out.CreateCommandBatch.Batch.Resolved)
	}
	if out.CreateCommandBatch.Batch.TargetKind != string(model.BatchTargetGroup) {
		t.Fatalf("expected a GROUP batch, got %q", out.CreateCommandBatch.Batch.TargetKind)
	}
}

// TestDeviceListBatchDoesNotRequireDeviceRead is the counterweight: the gate above must
// be scoped to group targets, or it silently raises the bar on the common path.
func TestDeviceListBatchDoesNotRequireDeviceRead(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	if got := createBatch(t, ctx, listRequest("wire-list-ok", "pump-a")); got.CreateCommandBatch.Batch == nil {
		t.Fatalf("naming devices explicitly needs only command:write, got rejection %+v",
			got.CreateCommandBatch.Rejection)
	}
}

// TestBatchQueriesReadBackTheRecord covers the read side, including the batchToken
// filter that is the ONLY way to ask what a fleet write is doing now.
func TestBatchQueriesReadBackTheRecord(t *testing.T) {
	ctx, _ := newWireTestCtx(t)
	createBatch(t, ctx, listRequest("read-back", "pump-a", "pump-b"))

	const byToken = `query($tokens: [String!]!) {
	  commandBatchesByToken(tokens: $tokens) { token resolved accepted targetKind }
	}`
	data := exec(t, ctx, byToken, map[string]any{"tokens": []any{"read-back"}})
	var found struct {
		CommandBatchesByToken []struct {
			Token      string `json:"token"`
			Resolved   int    `json:"resolved"`
			Accepted   int    `json:"accepted"`
			TargetKind string `json:"targetKind"`
		} `json:"commandBatchesByToken"`
	}
	if err := json.Unmarshal(data, &found); err != nil {
		t.Fatalf("decoding commandBatchesByToken failed: %v", err)
	}
	if len(found.CommandBatchesByToken) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(found.CommandBatchesByToken))
	}
	if found.CommandBatchesByToken[0].Accepted != 2 {
		t.Fatalf("the stored count did not survive the read, got %+v", found.CommandBatchesByToken[0])
	}

	// The batchToken filter, bound through a variable like every real client.
	const commandsOfBatch = `query($criteria: CommandSearchCriteria!) {
	  commands(criteria: $criteria) { results { token deviceToken } }
	}`
	data = exec(t, ctx, commandsOfBatch, map[string]any{
		"criteria": map[string]any{
			"pageNumber": 1, "pageSize": 100, "batchToken": "read-back",
		},
	})
	var commands struct {
		Commands struct {
			Results []struct {
				Token       string `json:"token"`
				DeviceToken string `json:"deviceToken"`
			} `json:"results"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(data, &commands); err != nil {
		t.Fatalf("decoding commands failed: %v", err)
	}
	// Asserted on the ROWS, not the criteria: a filter that binds to nothing filters
	// nothing, which returns MORE rows rather than an error.
	if len(commands.Commands.Results) != 2 {
		t.Fatalf("expected the batch's 2 commands, got %d", len(commands.Commands.Results))
	}
}

// TestUnknownBatchInputFieldIsRejected pins the forked-library behaviour on THIS input.
//
// The fork exists because upstream silently DISCARDS input-object entries the schema does
// not define when they arrive through a variable. On this mutation that fail-open is
// especially bad: `allowPartial` misspelled would be dropped, the batch would take the
// safe default, and a caller who explicitly opted into a partial fan-out would get a
// whole-batch refusal it never asked for — or worse, a misspelled `groupToken` would leave
// a request that looks group-targeted refused as ambiguous.
func TestUnknownBatchInputFieldIsRejected(t *testing.T) {
	ctx, _ := newWireTestCtx(t)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, createBatchMutation, "", map[string]any{
		"request": map[string]any{
			"token": "wire-unknown-field", "name": "reboot", "allowPartial": false,
			"deviceTokens":  []any{"pump-a"},
			"allow_partial": true, // not a field in the schema
		},
	})
	if len(res.Errors) == 0 {
		t.Fatal("an unknown input-object field must be a REQUEST ERROR; silently dropping " +
			"it is the upstream fail-open this repo forks the library to fix")
	}
}
