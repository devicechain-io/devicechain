// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newBatchTestApi builds an Api carrying both command tables and their per-tenant token
// indexes.
//
// ⚠️ IT DOES NOT CREATE `uix_commands_batch_device`. That index is literal DDL in the
// migration and AutoMigrate does not reproduce it, so the intra-batch duplicate guard is
// NOT exercised by any test in this package — dedup is enforced here only by
// dedupeBatchTokens, which is exactly why that code has its own test rather than leaning on
// the schema. The index is verified where it can be: hack/migration-diff.sh against real
// Postgres.
func newBatchTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&Command{}, &CommandBatch{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, model := range []any{&Command{}, &CommandBatch{}} {
		if err := rdb.CreateTenantTokenIndex(db, model); err != nil {
			t.Fatalf("create tenant token index: %v", err)
		}
	}
	return NewApi(&rdb.RdbManager{Database: db})
}

// countingValidator records how many times the fleet gate was consulted and refuses the
// device tokens it was told to.
type countingValidator struct {
	calls   int
	devices int
	refuse  map[string]RejectionCode
}

func (v *countingValidator) ValidateEnqueueBatch(ctx context.Context, deviceTokens []string,
	commandKey string, payload []byte) ([]BatchDeviceRefusal, error) {
	v.calls++
	v.devices += len(deviceTokens)
	refusals := make([]BatchDeviceRefusal, 0)
	for _, token := range deviceTokens {
		if code, no := v.refuse[token]; no {
			refusals = append(refusals, BatchDeviceRefusal{
				DeviceToken: token, Code: code, Reason: "refused by test",
			})
		}
	}
	return refusals, nil
}

// failingValidator stands in for an unreachable device-management.
type failingValidator struct{ calls int }

func (v *failingValidator) ValidateEnqueueBatch(context.Context, []string, string, []byte) ([]BatchDeviceRefusal, error) {
	v.calls++
	return nil, errors.New("device-management is unreachable")
}

func deviceTokens(n int) []string {
	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		tokens = append(tokens, fmt.Sprintf("pump-%04d", i))
	}
	return tokens
}

func batchRequest(token string, tokens []string) *CommandBatchCreateRequest {
	return &CommandBatchCreateRequest{Token: token, Name: "reboot", DeviceTokens: &tokens}
}

// namedDevices builds the pointer-to-slice shape the request carries for its nullable
// GraphQL list.
func namedDevices(n int) *[]string {
	tokens := deviceTokens(n)
	return &tokens
}

// decodeRefusalCounts reads a batch's stored per-code totals back.
func decodeRefusalCounts(t *testing.T, batch *CommandBatch) []RefusalCount {
	t.Helper()
	if batch.RefusalCounts == nil {
		return nil
	}
	counts := make([]RefusalCount, 0)
	if err := json.Unmarshal(*batch.RefusalCounts, &counts); err != nil {
		t.Fatalf("decode refusal counts: %v", err)
	}
	return counts
}

// commandsOf loads the commands a batch created, in id order.
func commandsOf(t *testing.T, api *Api, ctx context.Context, batchToken string) []*Command {
	t.Helper()
	found := make([]*Command, 0)
	if err := api.RDB.DB(ctx).Where("batch_token = ?", batchToken).Order("id").
		Find(&found).Error; err != nil {
		t.Fatalf("load batch commands: %v", err)
	}
	return found
}

// 🔴 TestAReplayedBatchDoesNotResolveOrValidateAnything.
//
// The replay probe runs FIRST — before target resolution, before the fleet gate, before the
// ceiling — and this is the test that holds it there. The failure it guards is specific and
// nasty: a client whose request timed out retries with the same token, and if the ceiling or
// the gate ran first, the retry would be refused for a condition that is not true of it (it
// creates nothing) while its original batch sat there succeeded. It would also re-resolve
// and re-validate a ten-thousand-member group to reach that wrong answer.
func TestAReplayedBatchDoesNotResolveOrValidateAnything(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	validator := &countingValidator{}
	api.BatchValidator = validator

	first, err := api.CreateCommandBatch(ctx, batchRequest("nightly", deviceTokens(3)))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	callsAfterFirst := validator.calls
	if callsAfterFirst == 0 {
		t.Fatal("the fleet gate was never consulted on the first issue — the instrument " +
			"cannot detect a replay that re-validates")
	}

	// The same token, and deliberately a DIFFERENT device set: the token is the identity,
	// so the first write wins and the re-request must not mutate or extend it.
	second, err := api.CreateCommandBatch(ctx, batchRequest("nightly", deviceTokens(9)))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("replay created a new batch (%d vs %d)", second.ID, first.ID)
	}
	if second.Resolved != 3 || second.Accepted != 3 {
		t.Fatalf("replay reported resolved=%d accepted=%d; the original batch was 3/3 and a "+
			"re-request must not extend it", second.Resolved, second.Accepted)
	}
	if validator.calls != callsAfterFirst {
		t.Fatalf("the fleet gate ran %d extra times on a replay; the probe is not first",
			validator.calls-callsAfterFirst)
	}
	if got := len(commandsOf(t, api, ctx, "nightly")); got != 3 {
		t.Fatalf("a replay created %d commands, want the original 3", got)
	}
}

// 🔴 TestOneRefusedDeviceFailsTheWholeBatchAndCreatesNOTHING.
//
// Default is all-or-nothing, and "nothing" has to mean nothing — no batch row, no commands,
// and critically the TOKEN UNSPENT, so a corrected re-issue under the same token works. A
// batch row written before the refusal would claim the token forever and make every retry a
// replay of a batch that never sent anything.
func TestOneRefusedDeviceFailsTheWholeBatchAndCreatesNothing(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{
		// A RELAYED code, spelled as a literal on purpose: this service does not declare
		// the owner's vocabulary rejections and passes them through verbatim, including
		// values this build has never heard of.
		refuse: map[string]RejectionCode{"pump-0002": RejectionCode("COMMAND_NOT_IN_VOCABULARY")},
	}

	_, err := api.CreateCommandBatch(ctx, batchRequest("firmware", deviceTokens(5)))
	var refusal *BatchRejected
	if !errors.As(err, &refusal) {
		t.Fatalf("expected a batch rejection, got %v", err)
	}
	if refusal.Code != RejectBatchPartialRefused {
		t.Fatalf("code = %q, want %q", refusal.Code, RejectBatchPartialRefused)
	}
	// 🔑 The refused devices must travel WITH the rejection. Nothing was created, so there
	// is no record to read them from — without this the operator is told "some devices were
	// refused" and has to bisect a fleet by hand.
	if len(refusal.Refusals) != 1 || refusal.Refusals[0].DeviceToken != "pump-0002" {
		t.Fatalf("rejection did not name the offending device: %+v", refusal.Refusals)
	}
	if refusal.Resolved != 5 {
		t.Fatalf("resolved = %d, want 5 so the caller can say '1 of 5'", refusal.Resolved)
	}

	var batches int64
	if err := api.RDB.DB(ctx).Model(&CommandBatch{}).Count(&batches).Error; err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if batches != 0 {
		t.Fatal("a refused batch left a batch row behind; the token is now spent and every " +
			"corrected re-issue will replay an empty batch forever")
	}
	if got := len(commandsOf(t, api, ctx, "firmware")); got != 0 {
		t.Fatalf("a refused batch created %d commands", got)
	}
}

// TestAllowPartialAdmitsTheRefusedDevicesExceptAndRecordsThem. With the opt-in, the
// admissible devices go out and the refused ones are recorded rather than blocking.
func TestAllowPartialAdmitsTheRestAndRecordsTheRefusals(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{
		refuse: map[string]RejectionCode{"pump-0001": RejectionCode("COMMAND_NOT_IN_VOCABULARY")},
	}

	request := batchRequest("firmware", deviceTokens(4))
	request.AllowPartial = true
	batch, err := api.CreateCommandBatch(ctx, request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if batch.Resolved != 4 || batch.Accepted != 3 {
		t.Fatalf("resolved=%d accepted=%d, want 4/3", batch.Resolved, batch.Accepted)
	}
	commands := commandsOf(t, api, ctx, "firmware")
	if len(commands) != 3 {
		t.Fatalf("created %d commands, want 3", len(commands))
	}
	for _, cmd := range commands {
		if cmd.DeviceToken == "pump-0001" {
			t.Fatal("a refused device was commanded anyway")
		}
		if !cmd.BatchId.Valid || cmd.BatchId.Int64 != int64(batch.ID) {
			t.Fatalf("command %s carries batch id %v, want %d", cmd.Token, cmd.BatchId, batch.ID)
		}
		if !cmd.BatchToken.Valid || cmd.BatchToken.String != "firmware" {
			t.Fatalf("command %s carries batch token %v", cmd.Token, cmd.BatchToken)
		}
	}
	if batch.Refusals == nil {
		t.Fatal("the refused device was not recorded on the batch — with no command row and " +
			"no record entry, nothing anywhere says it was left out")
	}
}

// 🔴 TestTheCeilingBoundsTheWholeBatchNotEachCommand. The headroom check is the batch's
// size against remaining room, decided once — not a per-command check that would admit
// the batch one row at a time until it tipped over.
func TestTheCeilingBoundsTheWholeBatchNotEachCommand(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 5
	// Service-token class, so the bound under test is the ceiling — see machineryCtx.
	ctx := machineryCtx(core.WithTenant(context.Background(), "acme"))
	api.BatchValidator = &countingValidator{}

	_, err := api.CreateCommandBatch(ctx, batchRequest("toobig", deviceTokens(6)))
	var refusal *BatchRejected
	if !errors.As(err, &refusal) {
		t.Fatalf("expected a rejection, got %v", err)
	}
	if refusal.Code != RejectHeldCeilingExceeded {
		t.Fatalf("code = %q, want %q", refusal.Code, RejectHeldCeilingExceeded)
	}
	if got := len(commandsOf(t, api, ctx, "toobig")); got != 0 {
		t.Fatalf("an over-ceiling batch created %d commands; it must be all-or-nothing", got)
	}
}

// 🔑 TestAllowPartialFillsExactlyTheHeadroomInResolvedOrder.
//
// Two properties, and the ORDER one is what makes a partial batch explicable. The subset
// that goes out is the first N in the order the target resolved — the caller's own order for
// an explicit list — so an operator can predict and explain which machines acted. An
// arbitrary (map-ordered) subset would be indefensible for a physical actuation.
func TestAllowPartialFillsExactlyTheHeadroomInResolvedOrder(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 4
	// Service-token class, so the headroom under test is the ceiling itself rather than
	// the ceiling less the delivery machinery reserve — see machineryCtx.
	ctx := machineryCtx(core.WithTenant(context.Background(), "acme"))
	api.BatchValidator = &countingValidator{}

	// One command already outstanding, so the headroom is 3 rather than the whole ceiling.
	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "already", DeviceToken: "other-1", Name: "reboot",
	}); err != nil {
		t.Fatalf("seed outstanding command: %v", err)
	}

	request := batchRequest("partial", deviceTokens(6))
	request.AllowPartial = true
	batch, err := api.CreateCommandBatch(ctx, request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if batch.Accepted != 3 {
		t.Fatalf("accepted = %d, want exactly the remaining headroom of 3", batch.Accepted)
	}
	commands := commandsOf(t, api, ctx, "partial")
	got := make([]string, 0, len(commands))
	for _, cmd := range commands {
		got = append(got, cmd.DeviceToken)
	}
	want := []string{"pump-0000", "pump-0001", "pump-0002"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("admitted %v, want the FIRST %v in the caller's own order — an "+
				"arbitrary subset of a physical actuation cannot be explained to anyone",
				got, want)
		}
	}
	if batch.Resolved != 6 {
		t.Fatalf("resolved = %d, want 6 — the record must show what it saw, not what it sent",
			batch.Resolved)
	}
}

// 🔴 TestAnUnreachableGateFailsTheBatchWithTheTokenUNSPENT. A validation outage must not be
// reported to the client as "your command is invalid", and it must leave the batch token
// free so the identical request succeeds once the owner is back.
func TestAnUnreachableGateFailsTheBatchWithTheTokenUnspent(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	failing := &failingValidator{}
	api.BatchValidator = failing

	_, err := api.CreateCommandBatch(ctx, batchRequest("nightly", deviceTokens(3)))
	if err == nil {
		t.Fatal("an unreachable gate must fail the batch")
	}
	var refusal *BatchRejected
	if errors.As(err, &refusal) {
		t.Fatal("an availability failure surfaced as a decided REJECTION; the client would " +
			"be told its command is invalid during an outage and go chasing a correct payload")
	}

	// The token must still be free.
	api.BatchValidator = &countingValidator{}
	batch, err := api.CreateCommandBatch(ctx, batchRequest("nightly", deviceTokens(3)))
	if err != nil {
		t.Fatalf("the retry after recovery failed: %v", err)
	}
	if batch.Accepted != 3 {
		t.Fatalf("accepted = %d after recovery, want 3", batch.Accepted)
	}
}

// TestABatchMustNameExactlyOneTarget. Both or neither is refused rather than resolved by
// precedence: a caller that sent both does not know what it asked for, and silently
// preferring one would fan a physical actuation across whichever set the implementation
// happened to favour.
func TestABatchMustNameExactlyOneTarget(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	group := "fleet"

	cases := map[string]*CommandBatchCreateRequest{
		"neither": {Token: "t1", Name: "reboot"},
		"both": {Token: "t2", Name: "reboot", DeviceTokens: namedDevices(2),
			GroupToken: &group},
		"version without a group": {Token: "t3", Name: "reboot",
			DeviceTokens: namedDevices(2), GroupVersion: func() *int32 { v := int32(1); return &v }()},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := api.CreateCommandBatch(ctx, request)
			var refusal *EnqueueRejected
			if !errors.As(err, &refusal) {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if refusal.Code != RejectBatchTargetAmbiguous {
				t.Fatalf("code = %q, want %q", refusal.Code, RejectBatchTargetAmbiguous)
			}
		})
	}
}

// TestADuplicateDeviceIsCommandedOnce. A caller that built its list by concatenating two
// groups must not have the device counted twice against the headroom, nor collide on the
// intra-batch unique index and turn a copy-paste into a server error.
func TestADuplicateDeviceIsCommandedOnce(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}

	batch, err := api.CreateCommandBatch(ctx, batchRequest("dupes",
		[]string{"pump-1", "pump-2", "pump-1", "pump-2", "pump-3"}))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if batch.Resolved != 3 || batch.Accepted != 3 {
		t.Fatalf("resolved=%d accepted=%d, want 3/3 after deduplication",
			batch.Resolved, batch.Accepted)
	}
	if got := len(commandsOf(t, api, ctx, "dupes")); got != 3 {
		t.Fatalf("created %d commands for 3 distinct devices", got)
	}
}

// 🔴 TestABatchCannotSeeAnotherTenantsBatchToken. Batch tokens are unique per tenant, so two
// tenants may each run a "nightly". If the replay probe lost its scope, one tenant's issue
// would return another tenant's batch — and report success for a fleet write it never made.
func TestABatchCannotSeeAnotherTenantsBatchToken(t *testing.T) {
	api := newBatchTestApi(t)
	api.BatchValidator = &countingValidator{}
	acme := core.WithTenant(context.Background(), "acme")
	other := core.WithTenant(context.Background(), "other")

	mine, err := api.CreateCommandBatch(acme, batchRequest("nightly", deviceTokens(2)))
	if err != nil {
		t.Fatalf("create acme batch: %v", err)
	}
	theirs, err := api.CreateCommandBatch(other, batchRequest("nightly", deviceTokens(5)))
	if err != nil {
		t.Fatalf("create other batch: %v", err)
	}

	if theirs.ID == mine.ID {
		t.Fatal("the second tenant's issue replayed the FIRST tenant's batch — it would be " +
			"told its fleet write succeeded while nothing was sent to any of its devices")
	}
	if theirs.Accepted != 5 {
		t.Fatalf("the other tenant's batch accepted %d, want 5", theirs.Accepted)
	}
}

// TestTheStoredRefusalsAreBoundedButTheCountsAreNot. A dynamic group can resolve to tens of
// thousands of devices, so the refusal sample is capped — but the per-code totals must stay
// complete, or `resolved = accepted + refusals` stops closing and the record can no longer
// account for the devices it did not command.
func TestTheStoredRefusalsAreBoundedButTheCountsAreNot(t *testing.T) {
	refusals := make([]BatchDeviceRefusal, 0, maxStoredRefusalsPerCode*2)
	for i := 0; i < maxStoredRefusalsPerCode*2; i++ {
		refusals = append(refusals, BatchDeviceRefusal{
			DeviceToken: fmt.Sprintf("pump-%d", i), Code: RejectionCode("DEVICE_NOT_FOUND"), Reason: "gone",
		})
	}
	kept, counts := boundRefusals(refusals)

	if len(kept) != maxStoredRefusalsPerCode {
		t.Fatalf("kept %d refusals, want the cap of %d", len(kept), maxStoredRefusalsPerCode)
	}
	if len(counts) != 1 || counts[0].Count != maxStoredRefusalsPerCode*2 {
		t.Fatalf("counts = %+v; the TOTAL must be complete even though the sample is not",
			counts)
	}
}
