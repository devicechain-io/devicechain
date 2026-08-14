// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// pagingResolver is a fake GroupTargetResolver that serves a fixed member list in keyset
// pages, recording how it was walked.
type pagingResolver struct {
	members []string
	pages   int
	// rejectWith, when set, makes the FIRST call answer a decided refusal.
	rejectWith string
	// alwaysCursor makes every page claim there is another, which is what the walk's
	// termination fuse exists to survive.
	alwaysCursor bool
	// shortPages caps how many members a page returns regardless of the requested limit —
	// the legal behaviour of an owner that post-filters, or of a group churning mid-walk.
	shortPages int
	version    *int32
	// lastVersionAsked records the version the caller pinned.
	lastVersionAsked *int32
	versionSeen      bool
}

func (r *pagingResolver) ResolveGroupTargets(ctx context.Context, groupToken string,
	version *int32, afterCursor string, limit int) (*GroupTargetPage, error) {
	r.pages++
	r.lastVersionAsked = version
	r.versionSeen = true
	if r.rejectWith != "" {
		return &GroupTargetPage{Rejected: true, Code: "GROUP_NOT_A_DEVICE_GROUP", Reason: r.rejectWith}, nil
	}
	start := 0
	if afterCursor != "" {
		if _, err := fmt.Sscanf(afterCursor, "%d", &start); err != nil {
			return nil, fmt.Errorf("bad cursor %q", afterCursor)
		}
	}
	if r.shortPages > 0 && r.shortPages < limit {
		limit = r.shortPages
	}
	end := start + limit
	if end > len(r.members) {
		end = len(r.members)
	}
	page := &GroupTargetPage{DeviceTokens: r.members[start:end], ResolvedVersion: r.version}
	if r.alwaysCursor || end < len(r.members) {
		page.NextCursor = fmt.Sprintf("%d", end)
	}
	return page, nil
}

// erroringResolver stands in for an unreachable device-management.
type erroringResolver struct{}

func (erroringResolver) ResolveGroupTargets(context.Context, string, *int32, string, int) (*GroupTargetPage, error) {
	return nil, errors.New("device-management is unreachable")
}

func groupRequest(token, group string) *CommandBatchCreateRequest {
	return &CommandBatchCreateRequest{Token: token, Name: "reboot", GroupToken: &group}
}

// 🔴 TestAGroupTargetIsActuallyWALKED. Group resolution had NO test at all: a mutation that
// made it return an empty target set without ever calling the resolver passed the entire
// suite. That is precisely the failure the code's own comment calls the worst available
// outcome — a fleet write that reached nobody, reported as a success.
func TestAGroupTargetIsActuallyWalked(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	// More members than one page holds, so the walk has to follow its own cursor.
	members := make([]string, 0, groupTargetPageSize+250)
	for i := 0; i < groupTargetPageSize+250; i++ {
		members = append(members, fmt.Sprintf("pump-%05d", i))
	}
	resolver := &pagingResolver{members: members}
	api.GroupTargetResolver = resolver

	batch, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if resolver.pages < 2 {
		t.Fatalf("the resolver was called %d time(s); a member set larger than one page must "+
			"be walked across pages", resolver.pages)
	}
	if batch.Resolved != len(members) || batch.Accepted != len(members) {
		t.Fatalf("resolved=%d accepted=%d, want %d for both", batch.Resolved, batch.Accepted,
			len(members))
	}
	if batch.TargetKind != BatchTargetGroup.String() {
		t.Fatalf("targetKind = %q, want %q", batch.TargetKind, BatchTargetGroup)
	}
	if !batch.GroupToken.Valid || batch.GroupToken.String != "fleet" {
		t.Fatalf("the batch did not record which group it fired at: %+v", batch.GroupToken)
	}
	if got := len(commandsOf(t, api, ctx, "nightly")); got != len(members) {
		t.Fatalf("created %d commands for %d members", got, len(members))
	}
}

// TestTheResolvedGroupVersionIsRecorded. The version is the only thing that can answer
// "what did this group MEAN when the batch fired?" after someone edits the selector, so a
// batch that resolved a frozen version and did not store it has lost the audit trail the
// record exists to provide.
func TestTheResolvedGroupVersionIsRecorded(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	version := int32(7)
	api.GroupTargetResolver = &pagingResolver{members: []string{"pump-1", "pump-2"}, version: &version}

	batch, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !batch.GroupVersion.Valid || batch.GroupVersion.Int32 != 7 {
		t.Fatalf("groupVersion = %+v, want 7", batch.GroupVersion)
	}
}

// TestAnUnusableGroupIsRelayedAsADecidedRejection. The owner decides whether a group may be
// commanded (it collects devices or it does not, it is published or it is not); this service
// must relay that as a rejection the caller can act on, NOT as an availability error, and
// must not create a batch.
func TestAnUnusableGroupIsRelayedAsADecidedRejection(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	api.GroupTargetResolver = &pagingResolver{rejectWith: "entity group \"north\" collects areas, not devices"}

	_, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "north"))
	var refusal *EnqueueRejected
	if !errors.As(err, &refusal) {
		t.Fatalf("expected a decided rejection, got %v", err)
	}
	if refusal.Code != RejectGroupUnusable {
		t.Fatalf("code = %q, want %q", refusal.Code, RejectGroupUnusable)
	}
	if !strings.Contains(refusal.Reason, "not devices") {
		t.Fatalf("the owner's reason was not relayed: %q", refusal.Reason)
	}
	var batches int64
	if err := api.RDB.DB(ctx).Model(&CommandBatch{}).Count(&batches).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if batches != 0 {
		t.Fatal("an unusable group still created a batch record")
	}
}

// TestAnUnreachableGroupResolverIsNOTARejection. An outage must not be reported to the
// client as "your group is wrong" — that sends an operator to fix a group that is fine.
func TestAnUnreachableGroupResolverIsNotARejection(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	api.GroupTargetResolver = erroringResolver{}

	_, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	if err == nil {
		t.Fatal("an unreachable resolver must fail the batch")
	}
	var refusal *EnqueueRejected
	if errors.As(err, &refusal) {
		t.Fatal("an availability failure surfaced as a decided rejection")
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("the internal failure leaked to the client: %q", err)
	}
}

// TestAGroupTargetWithNoResolverFailsClosed. Nil must not mean "resolves to nothing": an
// empty batch would report success for a fleet write that reached nobody.
func TestAGroupTargetWithNoResolverFailsClosed(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}

	if _, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet")); err == nil {
		t.Fatal("a group target with no resolver wired must FAIL, not resolve to an empty batch")
	}
}

// TestAGroupThatNeverStopsPagingTripsTheFuse rather than spinning inside a request. The
// fuse is the only thing standing between a broken pagination contract and an unbounded
// loop, and it must be an ERROR: silently returning a short target set would report a
// fleet write as complete when it commanded a fraction of the fleet.
func TestAGroupThatNeverStopsPagingTripsTheFuse(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}
	api.GroupTargetResolver = &pagingResolver{members: []string{"pump-1"}, alwaysCursor: true}

	_, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	if err == nil {
		t.Fatal("a resolver that always claims another page must trip the fuse")
	}
	if !strings.Contains(err.Error(), "did not terminate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// 🔴 TestAGroupReturningSHORTPagesStillResolvesCompletely.
//
// This is the regression the barren-page fuse replaced a page-count fuse for. Short pages
// are LEGAL and expected — the owner post-filters, and membership churns mid-walk — but a
// fuse counting TOTAL pages assumed every page came back full, so a perfectly good fleet
// returning half-full pages needed more pages than the bound allowed and died with
// "resolution did not terminate". A walk that keeps finding devices is working, however
// many pages it takes; only one that keeps being handed a cursor while adding nobody is
// broken.
func TestAGroupReturningShortPagesStillResolvesCompletely(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}

	// Enough members that, at this page size, the walk needs far more pages than any
	// count-based bound derived from groupTargetPageSize would have permitted.
	members := make([]string, 0, 300)
	for i := 0; i < 300; i++ {
		members = append(members, fmt.Sprintf("pump-%05d", i))
	}
	resolver := &pagingResolver{members: members, shortPages: 3}
	api.GroupTargetResolver = resolver

	batch, err := api.CreateCommandBatch(ctx, groupRequest("nightly", "fleet"))
	if err != nil {
		t.Fatalf("a group returning short pages failed to resolve: %v", err)
	}
	if batch.Resolved != len(members) {
		t.Fatalf("resolved %d of %d members", batch.Resolved, len(members))
	}
	if resolver.pages < 100 {
		t.Fatalf("the walk took %d pages; the fixture was meant to force many short ones",
			resolver.pages)
	}
}

// 🔴 TestEVERYChunkOfALargeFleetIsValidated. Validation is chunked, and a mutation that
// validated only the FIRST chunk passed the whole suite — every other test used fewer than
// ten devices. A regression there sends thousands of devices a command the vocabulary gate
// never saw, which is the entire purpose of the gate.
func TestEveryChunkOfALargeFleetIsValidated(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = batchValidationChunk * 3
	// Service-token class: the ceiling here only has to be big enough not to interfere,
	// and the reserve would quietly shrink it. See machineryCtx.
	ctx := machineryCtx(core.WithTenant(context.Background(), "acme"))
	validator := &countingValidator{}
	api.BatchValidator = validator

	total := batchValidationChunk + 137 // deliberately not a whole number of chunks
	tokens := make([]string, 0, total)
	for i := 0; i < total; i++ {
		tokens = append(tokens, fmt.Sprintf("pump-%05d", i))
	}

	if _, err := api.CreateCommandBatch(ctx, batchRequest("bigfleet", tokens)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if validator.devices != total {
		t.Fatalf("the gate saw %d of %d devices — the tail of the fleet was enqueued without "+
			"ever being validated", validator.devices, total)
	}
	if validator.calls < 2 {
		t.Fatalf("the gate was called %d time(s) for %d devices; it must be chunked",
			validator.calls, total)
	}
}

// TestARequestNamingTooManyDevicesIsRefused before anything looks at it. The bound is input
// validation, not governance: its job is to stop a request from being enormous before the
// ceiling can even be consulted.
func TestARequestNamingTooManyDevicesIsRefused(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	validator := &countingValidator{}
	api.BatchValidator = validator

	tokens := make([]string, 0, MaxBatchDeviceTokens+1)
	for i := 0; i <= MaxBatchDeviceTokens; i++ {
		tokens = append(tokens, fmt.Sprintf("pump-%06d", i))
	}

	_, err := api.CreateCommandBatch(ctx, batchRequest("toomany", tokens))
	var refusal *EnqueueRejected
	if !errors.As(err, &refusal) {
		t.Fatalf("expected a rejection, got %v", err)
	}
	if refusal.Code != RejectBatchTooLarge {
		t.Fatalf("code = %q, want %q", refusal.Code, RejectBatchTooLarge)
	}
	if validator.calls != 0 {
		t.Fatal("an over-large request reached the fleet gate; the bound exists to refuse it " +
			"BEFORE the platform does work proportional to it")
	}
}

// 🔑 TestAPartialBatchAccountsForEveryDeviceItDidNotCommand.
//
// `resolved = accepted + total refusals` is the invariant that makes the record
// self-auditing, and it is asserted nowhere else: a mutation deleting the over-headroom
// refusal loop passed the suite, because the existing partial test checks accepted, order
// and resolved but never looks at what the record says about the devices left out.
func TestAPartialBatchAccountsForEveryDeviceItDidNotCommand(t *testing.T) {
	api := newBatchTestApi(t)
	api.DefaultHeldCommandCeiling = 3
	// Service-token class, so the headroom under test is the ceiling itself rather than
	// the ceiling less the delivery machinery reserve — see machineryCtx.
	ctx := machineryCtx(core.WithTenant(context.Background(), "acme"))
	api.BatchValidator = &countingValidator{}

	request := batchRequest("partial", deviceTokens(8))
	request.AllowPartial = true
	batch, err := api.CreateCommandBatch(ctx, request)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if batch.Accepted != 3 || batch.Resolved != 8 {
		t.Fatalf("accepted=%d resolved=%d, want 3/8", batch.Accepted, batch.Resolved)
	}
	if batch.RefusalCounts == nil {
		t.Fatal("five devices were not commanded and the record accounts for none of them — " +
			"resolved = accepted + refusals does not close, so nothing can audit the gap")
	}
	counts := decodeRefusalCounts(t, batch)
	total := 0
	for _, c := range counts {
		total += c.Count
	}
	if batch.Accepted+total != batch.Resolved {
		t.Fatalf("accepted(%d) + refusals(%d) = %d, want resolved(%d)",
			batch.Accepted, total, batch.Accepted+total, batch.Resolved)
	}
	if len(counts) != 1 || counts[0].Code != RejectHeldCeilingExceeded {
		t.Fatalf("refusal counts = %+v, want all HELD_CEILING_EXCEEDED", counts)
	}
}

// 🔴 TestTwoBatchesSharingALongTokenPrefixDoNotCollide.
//
// Command tokens used to be minted from the batch token TRUNCATED to 112 characters, so two
// legal batch tokens sharing a 112-character prefix produced byte-identical command tokens:
// the second batch died on the (tenant_id, token) unique index as a sanitized server error,
// and every retry failed identically forever because the input that caused it is the input.
// Minting from the batch's row id makes the property structural.
func TestTwoBatchesSharingALongTokenPrefixDoNotCollide(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}

	prefix := strings.Repeat("a", 112)
	first, err := api.CreateCommandBatch(ctx, batchRequest(prefix+"one", deviceTokens(2)))
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	second, err := api.CreateCommandBatch(ctx, batchRequest(prefix+"two", deviceTokens(2)))
	if err != nil {
		t.Fatalf("a second batch sharing the first's %d-character prefix failed: %v", len(prefix), err)
	}

	if first.ID == second.ID {
		t.Fatal("the two batches are the same row")
	}
	if second.Accepted != 2 {
		t.Fatalf("second batch accepted %d, want 2", second.Accepted)
	}
	if got := len(commandsOf(t, api, ctx, prefix+"two")); got != 2 {
		t.Fatalf("second batch created %d commands", got)
	}
}

// 🔴 TestAClientCannotBeHandedABatchCommandAsItsOwnREPLAY.
//
// A command token is a client-chosen idempotency key, and CreateCommand answers a token that
// already names a live command by returning THAT command. Batch commands carry tokens the
// PLATFORM minted into the same column — so a client issuing a command under a colliding
// token used to get back somebody else's actuation, for a different device and a different
// command name, reported as a successful replay with nothing created and no error. The
// minter's output is predictable, so this was reachable by accident.
func TestAClientCannotBeHandedABatchCommandAsItsOwnReplay(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	api.BatchValidator = &countingValidator{}

	batch, err := api.CreateCommandBatch(ctx, batchRequest("nightly", []string{"fleet-device"}))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	minted := batchCommandToken(batch.ID, 0)

	_, err = api.CreateCommand(ctx, &CommandCreateRequest{
		Token: minted, DeviceToken: "my-own-device", Name: "shutdown",
	})
	var refusal *EnqueueRejected
	if !errors.As(err, &refusal) {
		t.Fatalf("issuing a command under a batch-minted token returned %v; it must be "+
			"refused, never answered with the batch's command", err)
	}
	if refusal.Code != RejectTokenInUse {
		t.Fatalf("code = %q, want %q", refusal.Code, RejectTokenInUse)
	}

	// And the batch's own command is untouched: still its device, still its name.
	commands := commandsOf(t, api, ctx, "nightly")
	if len(commands) != 1 || commands[0].DeviceToken != "fleet-device" || commands[0].Name != "reboot" {
		t.Fatalf("the batch's command was disturbed: %+v", commands)
	}
}

// TestAnOrdinaryClientReplayStillWorks is the counterweight to the test above. Excluding
// batch rows from the replay probe must not break the idempotency the probe exists for —
// a client re-issuing its OWN token must still get its original command back.
func TestAnOrdinaryClientReplayStillWorks(t *testing.T) {
	api := newBatchTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")

	first, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "mine", DeviceToken: "pump-1", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "mine", DeviceToken: "pump-9", Name: "shutdown",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("a client's own replay created a second command (%d vs %d)", second.ID, first.ID)
	}
	if second.DeviceToken != "pump-1" || second.Name != "reboot" {
		t.Fatalf("the replay returned mutated fields: %+v", second)
	}
}
