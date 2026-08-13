// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// allStatuses is every status this build declares. It is written out by hand
// rather than derived from terminalStatuses+nonTerminalStatuses, because those
// two lists are the thing under test here: deriving the expectation from the
// implementation would make every assertion below vacuous.
var allStatuses = []CommandStatus{
	CommandQueued, CommandHeld, CommandSent,
	CommandSuccessful, CommandFailed,
	CommandTimeout, CommandExpired, CommandCancelled,
}

// TestEveryStatusIsClassifiedExactlyOnce is the guard on the split itself.
//
// A status that lands in NEITHER list reads as unknown: Valid() rejects it while
// the row persists happily, and Terminal() calls it non-terminal, so the sweep
// keeps reconsidering a command nothing can finish. A status in BOTH is a
// contradiction that the two readers below would resolve differently. Neither
// mistake produces an error at the point it is made — this test is where it
// surfaces.
func TestEveryStatusIsClassifiedExactlyOnce(t *testing.T) {
	for _, s := range allStatuses {
		inTerminal, inNonTerminal := 0, 0
		for _, t := range terminalStatuses {
			if s == t {
				inTerminal++
			}
		}
		for _, n := range nonTerminalStatuses {
			if s == n {
				inNonTerminal++
			}
		}
		if inTerminal+inNonTerminal != 1 {
			t.Fatalf("%s appears in terminalStatuses %d time(s) and nonTerminalStatuses %d time(s); "+
				"every status must be classified exactly once", s, inTerminal, inNonTerminal)
		}
		if !s.Valid() {
			t.Fatalf("%s is declared but Valid() rejects it", s)
		}
	}
	if got := len(terminalStatuses) + len(nonTerminalStatuses); got != len(allStatuses) {
		t.Fatalf("the two classification lists hold %d statuses but %d are declared; "+
			"a status was added to a list without being declared here", got, len(allStatuses))
	}
}

// TestTerminalStatusStringsMatchesTerminal pins the derivation that replaced a
// second hand-written list.
//
// 🔴 This is the highest-value assertion in the file. terminalStatusStrings()
// builds the "status NOT IN (…)" guard used by MarkResponse, CancelCommand and
// both of ExpireStale's writes, while CommandStatus.Terminal() backs the
// in-process fast paths. They used to be independent lists of the same set, and
// when two such lists disagree nothing fails: the fast path returns a row as
// finished while the SQL guard still lets a sweep overwrite it, so a cancelled
// command silently becomes TIMEOUT. Deriving one from the other is the fix; this
// test is what stops someone re-splitting them.
func TestTerminalStatusStringsMatchesTerminal(t *testing.T) {
	fromStrings := terminalStatusStrings()
	sort.Strings(fromStrings)

	fromPredicate := make([]string, 0, len(allStatuses))
	for _, s := range allStatuses {
		if s.Terminal() {
			fromPredicate = append(fromPredicate, s.String())
		}
	}
	sort.Strings(fromPredicate)

	if len(fromStrings) != len(fromPredicate) {
		t.Fatalf("terminalStatusStrings() = %v but Terminal() says %v", fromStrings, fromPredicate)
	}
	for i := range fromStrings {
		if fromStrings[i] != fromPredicate[i] {
			t.Fatalf("terminalStatusStrings() = %v but Terminal() says %v", fromStrings, fromPredicate)
		}
	}
}

// TestDispatchableStatusesAreNonTerminal guards the other shared set.
//
// dispatchableStatusStrings() is both the from-state guard for MarkSent and the
// selection predicate for PendingCommands. If it ever admitted a terminal state,
// the sweep would hand out finished commands and re-actuate hardware; if it and
// PendingCommands drifted apart, the sweep would either publish rows it cannot
// then mark sent (republishing them every tick forever) or hold rows nothing
// drains.
func TestDispatchableStatusesAreNonTerminal(t *testing.T) {
	dispatchable := dispatchableStatusStrings()
	if len(dispatchable) == 0 {
		t.Fatal("no dispatchable statuses; the delivery sweep would never publish anything")
	}
	for _, s := range dispatchable {
		if CommandStatus(s).Terminal() {
			t.Fatalf("%s is dispatchable AND terminal; the sweep would re-actuate a finished command", s)
		}
		if !CommandStatus(s).Valid() {
			t.Fatalf("%s is dispatchable but not a known status", s)
		}
	}
	// SENT must NOT be dispatchable: it has already gone out. Including it would
	// make the sweep republish every unanswered command on every tick.
	for _, s := range dispatchable {
		if CommandStatus(s) == CommandSent {
			t.Fatal("SENT must not be dispatchable; the sweep would republish every in-flight command each tick")
		}
	}
}

// TestExpiredTerminalForMapsHoldToExpired pins the mapping that decides which
// terminal a lapsed command reaches — the single place the platform says whether
// a command's death was its own fault or the device's.
func TestExpiredTerminalForMapsHoldToExpired(t *testing.T) {
	cases := map[CommandStatus]CommandStatus{
		// Never dispatched: the platform ran out of time.
		CommandQueued: CommandExpired,
		CommandHeld:   CommandExpired,
		// Dispatched and unanswered: the device did not reply.
		CommandSent: CommandTimeout,
	}
	for from, want := range cases {
		if got := expiredTerminalFor(from.String()); got != want.String() {
			t.Fatalf("a command lapsing from %s becomes %s, want %s", from, got, want)
		}
	}
}

// TestExpireLosesToARowThatMovedOnAfterTheScan is the guard on the from-state
// predicate in expireOne.
//
// ExpireStale SELECTs a batch and then writes each row, and the terminal it writes
// depends on the state the SCAN saw. If the row moves on in that gap — a wake
// drain claiming it, or the delivery sweep publishing it — a write guarded only on
// "still non-terminal" would stamp EXPIRED ("never dispatched") on a command that
// was physically dispatched microseconds earlier, and the device's imminent answer
// would then be dropped by MarkResponse's terminal guard. That is a lost response
// AND a count filed under the wrong state.
//
// The gap cannot be staged through the public API, which is why expireOne exists
// as its own seam: here the row is moved underneath the stale from-state directly.
func TestExpireLosesToARowThatMovedOnAfterTheScan(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "raced-expiry", DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	if err := forceStatus(api, ctx, created.ID, CommandHeld); err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	// The sweep scanned it as HELD. Before its write lands, a drain claims it.
	won, err := api.MarkSentByToken(ctx, "raced-expiry")
	if err != nil || !won {
		t.Fatalf("staging the claim failed: won=%v err=%v", won, err)
	}

	// The sweep's write, carrying the now-stale from-state.
	affected, err := api.expireOne(ctx, created.ID, CommandHeld.String(), CommandExpired.String())
	if err != nil {
		t.Fatalf("expireOne failed: %v", err)
	}
	if affected != 0 {
		t.Fatal("expiry must LOSE to a row that moved on after the scan; " +
			"winning stamps EXPIRED on a command that was just dispatched and drops the response that follows")
	}
	assertStatus(t, api, ctx, "raced-expiry", CommandSent)

	// And the device's answer still lands, which is the consequence that actually
	// matters — a row wrongly expired here would swallow it.
	if _, err := api.MarkResponse(ctx, "raced-expiry", true, nil, nil); err != nil {
		t.Fatalf("MarkResponse failed: %v", err)
	}
	assertStatus(t, api, ctx, "raced-expiry", CommandSuccessful)
}

// TestMarkSentReleasesAHeldCommand verifies the release transition: a hold is
// lifted by DISPATCHING it, straight from HELD to SENT.
//
// Routing the release back through QUEUED instead would open a window in which
// the row is indistinguishable from one never yet considered, and would leave a
// held command briefly countable as fresh work.
func TestMarkSentReleasesAHeldCommand(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "held-release", DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	if err := forceStatus(api, ctx, created.ID, CommandHeld); err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	got, err := api.MarkSent(ctx, created.ID)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if got.Status != CommandSent.String() {
		t.Fatalf("a held command must be releasable to SENT, got %s", got.Status)
	}
	if !got.SentTime.Valid {
		t.Fatalf("releasing a hold must stamp SentTime")
	}
}

// TestMarkSentByTokenClaimsExactlyOnce is the guard on the claim a dispatcher
// makes before it actuates hardware.
//
// The LwM2M wake drain issues a device's held commands over the CoAP session it
// just opened, outside the delivery sweep. It must claim each row first, because
// the sweep publishes anything still dispatchable — an unclaimed dispatch would
// leave the row HELD for the next tick to publish a second time, and a command is
// a physical movement of real equipment. "Exactly once" is the whole contract:
// the second caller must be told NO.
func TestMarkSentByTokenClaimsExactlyOnce(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	created, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "claim-me", DeviceToken: "d", Name: "reboot",
	})
	if err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	if err := forceStatus(api, ctx, created.ID, CommandHeld); err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	won, err := api.MarkSentByToken(ctx, "claim-me")
	if err != nil {
		t.Fatalf("MarkSentByToken failed: %v", err)
	}
	if !won {
		t.Fatal("the first claim on a held command must win")
	}
	assertStatus(t, api, ctx, "claim-me", CommandSent)

	// A second claimer — the sweep, or another replica — must lose. If this ever
	// returns true the caller dispatches again and the device acts twice.
	won, err = api.MarkSentByToken(ctx, "claim-me")
	if err != nil {
		t.Fatalf("MarkSentByToken failed: %v", err)
	}
	if won {
		t.Fatal("a second claim on an already-SENT command must LOSE; winning it re-actuates the device")
	}
}

// TestMarkSentByTokenLosesOnTerminal verifies a claim cannot resurrect a finished
// command — a cancelled command must never be dispatched.
func TestMarkSentByTokenLosesOnTerminal(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	if _, err := api.CreateCommand(ctx, &CommandCreateRequest{
		Token: "cancelled-claim", DeviceToken: "d", Name: "reboot",
	}); err != nil {
		t.Fatalf("CreateCommand failed: %v", err)
	}
	if _, err := api.CancelCommand(ctx, "cancelled-claim"); err != nil {
		t.Fatalf("CancelCommand failed: %v", err)
	}

	won, err := api.MarkSentByToken(ctx, "cancelled-claim")
	if err != nil {
		t.Fatalf("MarkSentByToken failed: %v", err)
	}
	if won {
		t.Fatal("claiming a CANCELLED command must lose; winning it dispatches a command an operator called off")
	}
	assertStatus(t, api, ctx, "cancelled-claim", CommandCancelled)
}

// TestCancelRecordsCancelledFromEveryLiveState verifies cancellation writes
// CANCELLED — its own terminal — from each state a live command can be in.
//
// It used to write EXPIRED, which conflated "a human called it off" with "the
// platform ran out of time to send it". Those are different actors, and an
// operator auditing a fleet needs to tell them apart: a run of EXPIRED means
// delivery is failing, a run of CANCELLED means people keep changing their minds.
func TestCancelRecordsCancelledFromEveryLiveState(t *testing.T) {
	for _, from := range []CommandStatus{CommandQueued, CommandHeld, CommandSent} {
		t.Run(from.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")

			created, err := api.CreateCommand(ctx, &CommandCreateRequest{
				Token: "cancel-" + from.String(), DeviceToken: "d", Name: "reboot",
			})
			if err != nil {
				t.Fatalf("CreateCommand failed: %v", err)
			}
			if from != CommandQueued {
				if err := forceStatus(api, ctx, created.ID, from); err != nil {
					t.Fatalf("force %s failed: %v", from, err)
				}
			}

			got, err := api.CancelCommand(ctx, "cancel-"+from.String())
			if err != nil {
				t.Fatalf("CancelCommand failed: %v", err)
			}
			if got.Status != CommandCancelled.String() {
				t.Fatalf("cancelling a %s command recorded %s, want CANCELLED", from, got.Status)
			}
		})
	}
}

// TestPendingCommandsCoversTheDispatchableSet verifies the sweep's source is
// exactly the dispatchable set — QUEUED and HELD, and nothing else.
//
// HELD must be included: the sweep is what NOTICES a held command can now go out,
// because it is the recurring pass that re-reads presence. A hold the sweep could
// not see would never be drained by a device coming back, only by its TTL.
func TestPendingCommandsCoversTheDispatchableSet(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	sysctx := core.WithSystemContext(ctx)

	seed := func(token string, status CommandStatus) {
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{
			Token: token, DeviceToken: "d", Name: "reboot",
		})
		if err != nil {
			t.Fatalf("CreateCommand %s failed: %v", token, err)
		}
		if status != CommandQueued {
			if err := forceStatus(api, ctx, created.ID, status); err != nil {
				t.Fatalf("force %s failed: %v", status, err)
			}
		}
	}
	seed("p-queued", CommandQueued)
	seed("p-held", CommandHeld)
	seed("p-sent", CommandSent)
	seed("p-cancelled", CommandCancelled)
	seed("p-successful", CommandSuccessful)

	pending, err := api.PendingCommands(sysctx)
	if err != nil {
		t.Fatalf("PendingCommands failed: %v", err)
	}
	got := make(map[string]bool, len(pending))
	for _, c := range pending {
		got[c.Token] = true
	}

	for _, want := range []string{"p-queued", "p-held"} {
		if !got[want] {
			t.Fatalf("%s must be delivered by the sweep but PendingCommands omitted it (got %v)", want, got)
		}
	}
	for _, unwanted := range []string{"p-sent", "p-cancelled", "p-successful"} {
		if got[unwanted] {
			t.Fatalf("%s must NOT be redelivered but PendingCommands returned it (got %v)", unwanted, got)
		}
	}
}

// TestCommandsFilterByStatuses covers the multi-status search filter the LwM2M
// wake drain needs (it asks for HELD ∪ SENT in one query), including the empty
// list, which is deliberately "no filter" rather than "match nothing".
func TestCommandsFilterByStatuses(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seed := func(token string, status CommandStatus) {
		created, err := api.CreateCommand(ctx, &CommandCreateRequest{
			Token: token, DeviceToken: "d", Name: "reboot",
		})
		if err != nil {
			t.Fatalf("CreateCommand %s failed: %v", token, err)
		}
		if status != CommandQueued {
			if err := forceStatus(api, ctx, created.ID, status); err != nil {
				t.Fatalf("force %s failed: %v", status, err)
			}
		}
	}
	seed("f-queued", CommandQueued)
	seed("f-held", CommandHeld)
	seed("f-sent", CommandSent)
	seed("f-successful", CommandSuccessful)

	search := func(criteria CommandSearchCriteria) map[string]bool {
		t.Helper()
		criteria.Pagination = rdb.Pagination{PageNumber: 1, PageSize: 100}
		res, err := api.Commands(ctx, criteria)
		if err != nil {
			t.Fatalf("Commands failed: %v", err)
		}
		out := make(map[string]bool, len(res.Results))
		for _, c := range res.Results {
			out[c.Token] = true
		}
		return out
	}

	// The drain's actual query: the two states a waking device's backlog lives in.
	drain := []string{CommandHeld.String(), CommandSent.String()}
	got := search(CommandSearchCriteria{Statuses: &drain})
	if !got["f-held"] || !got["f-sent"] {
		t.Fatalf("statuses filter must return both HELD and SENT rows, got %v", got)
	}
	if got["f-queued"] || got["f-successful"] {
		t.Fatalf("statuses filter returned rows outside the requested set: %v", got)
	}

	// An empty list is "no filter". Honouring it literally would make the result
	// depend on how the driver renders an empty IN rather than on the query.
	empty := []string{}
	got = search(CommandSearchCriteria{Statuses: &empty})
	if !got["f-queued"] || !got["f-held"] || !got["f-sent"] || !got["f-successful"] {
		t.Fatalf("an empty statuses list must not filter anything out, got %v", got)
	}

	// Status and Statuses are ANDed, so a singular value outside the list yields
	// nothing rather than silently widening the result.
	queued := CommandQueued.String()
	got = search(CommandSearchCriteria{Status: &queued, Statuses: &drain})
	if len(got) != 0 {
		t.Fatalf("status and statuses must AND (intersection empty here), got %v", got)
	}
}
