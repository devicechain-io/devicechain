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
	CommandQueued, CommandHeld, CommandSent, CommandParked,
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
// builds the "status NOT IN (…)" guard used by both of ExpireStale's writes and by
// the stranded-SENT reconciler, while CommandStatus.Terminal() backs the in-process
// fast paths. They used to be independent lists of the same set, and
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

// TestDispatchableStatusesAreNonTerminal guards the two dispatch sets and, crucially,
// the RELATIONSHIP between them now that they have come apart.
//
// claimableStatusStrings() is the from-state guard for MarkSent; sweepableStatusStrings()
// is what the periodic sweep selects. If either ever admitted a terminal state, a
// finished command would be handed out and hardware re-actuated. And the sweep's set must
// stay a SUBSET of the claimable one: a row the sweep publishes but cannot then mark sent
// is republished on every tick forever.
//
// 🔑 Subset, NOT equality — that is what changed. HELD is claimable (the release paths
// claim held rows directly) but deliberately not sweepable (the gate exists so held rows
// are released by a device's return, not by polling).
func TestDispatchableStatusesAreNonTerminal(t *testing.T) {
	claimable := claimableStatusStrings()
	sweepable := sweepableStatusStrings()
	if len(claimable) == 0 {
		t.Fatal("no claimable statuses; no dispatcher could ever claim a command")
	}
	if len(sweepable) == 0 {
		t.Fatal("no sweepable statuses; the delivery sweep would never publish anything")
	}
	for _, s := range append(append([]string{}, claimable...), sweepable...) {
		if CommandStatus(s).Terminal() {
			t.Fatalf("%s is dispatchable AND terminal; a finished command would be re-actuated", s)
		}
		if !CommandStatus(s).Valid() {
			t.Fatalf("%s is dispatchable but not a known status", s)
		}
	}
	// The subset relation, which is the invariant the split has to preserve.
	claimSet := map[string]bool{}
	for _, s := range claimable {
		claimSet[s] = true
	}
	for _, s := range sweepable {
		if !claimSet[s] {
			t.Fatalf("the sweep selects %s but MarkSent will not claim it; every such row would be "+
				"published and republished on every tick forever", s)
		}
	}
	// HELD must be claimable but NOT sweepable. Both halves are load-bearing: drop it from
	// claimable and nothing can ever release a hold; add it to sweepable and the gate is
	// undone — the sweep goes back to re-reading an absent fleet's whole backlog every tick.
	if !claimSet[CommandHeld.String()] {
		t.Fatal("HELD is not claimable, so no release path could ever dispatch a held command")
	}
	for _, s := range sweepable {
		if CommandStatus(s) == CommandHeld {
			t.Fatal("HELD is sweepable; the gate is undone and the sweep re-reads the whole held backlog")
		}
	}
	// SENT must be in neither set: it has already gone out.
	for _, s := range append(append([]string{}, claimable...), sweepable...) {
		if CommandStatus(s) == CommandSent {
			t.Fatal("SENT must not be dispatchable; every in-flight command would be republished each tick")
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

	id := seedWithStatus(t, api, ctx, "raced-expiry", CommandHeld)

	// The sweep scanned it as HELD. Before its write lands, a drain claims it.
	won, err := api.MarkSentByToken(ctx, "raced-expiry")
	if err != nil || !won {
		t.Fatalf("staging the claim failed: won=%v err=%v", won, err)
	}

	// The sweep's write, carrying the now-stale from-state.
	expired, err := api.expireOne(ctx, id, CommandHeld.String(), CommandExpired.String())
	if err != nil {
		t.Fatalf("expireOne failed: %v", err)
	}
	if expired {
		t.Fatal("expiry must LOSE to a row that moved on after the scan; " +
			"winning stamps EXPIRED on a command that was just dispatched and drops the response that follows")
	}
	assertStatus(t, api, ctx, "raced-expiry", CommandSent)

	// And the device's answer still lands, which is the consequence that actually
	// matters — a row wrongly expired here would swallow it.
	if _, err := api.MarkResponse(ctx, "raced-expiry", "d", true, nil, nil); err != nil {
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

	id := seedWithStatus(t, api, ctx, "held-release", CommandHeld)

	_, claimed, err := api.MarkSent(ctx, id)
	if err != nil {
		t.Fatalf("MarkSent failed: %v", err)
	}
	if !claimed {
		t.Fatal("a held command must be claimable; otherwise nothing can ever release a hold")
	}
	got := loadOrFail(t, api, ctx, id)
	if got.Status != CommandSent.String() {
		t.Fatalf("a held command must be releasable to SENT, got %s", got.Status)
	}
	if !got.SentTime.Valid {
		t.Fatalf("releasing a hold must stamp SentTime")
	}

	// 🔑 And the release must be undoable, or a failed publish strands the row: SENT with
	// a sent_time, claimable by nobody, dying TIMEOUT for a command never delivered.
	released, err := api.ReleaseClaim(ctx, id)
	if err != nil {
		t.Fatalf("ReleaseClaim failed: %v", err)
	}
	if !released {
		t.Fatal("ReleaseClaim did not release a row it had just claimed")
	}
	back := loadOrFail(t, api, ctx, id)
	if back.Status != CommandQueued.String() {
		t.Fatalf("a released claim must return to QUEUED so the gate re-evaluates presence, got %s",
			back.Status)
	}
	if back.SentTime.Valid {
		t.Fatal("a released claim must clear SentTime; a sent_time on an undelivered command is the " +
			"record that makes TIMEOUT look justified")
	}
}

// TestReleaseClaimCannotResurrectAnAnsweredCommand pins the from-state guard.
//
// 🔴 THE RACE IS REAL AND THE COMMENT ON ReleaseClaim ALREADY DESCRIBED IT — it just had
// nothing checking it, which a mutation found by deleting the guard and watching every
// test stay green. A publish can land while its acknowledgement is lost: the dispatcher
// sees a failure and releases, but the device has already acted and answered, driving the
// row terminal. Without the guard the release drags that finished command back to QUEUED,
// where the next sweep tick dispatches it AGAIN — a second physical actuation caused by
// the very code written to prevent one.
func TestReleaseClaimCannotResurrectAnAnsweredCommand(t *testing.T) {
	for _, terminal := range []CommandStatus{
		CommandSuccessful, CommandFailed, CommandCancelled, CommandExpired, CommandTimeout,
	} {
		t.Run(string(terminal), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")
			id := seedWithStatus(t, api, ctx, "answered-"+string(terminal), terminal)

			released, err := api.ReleaseClaim(ctx, id)
			if err != nil {
				t.Fatalf("ReleaseClaim errored on a terminal command: %v", err)
			}
			if released {
				t.Fatalf("ReleaseClaim reported it released a %s command", terminal)
			}
			if got := loadOrFail(t, api, ctx, id); got.Status != terminal.String() {
				t.Fatalf("ReleaseClaim resurrected a %s command as %s; the next sweep tick would "+
					"dispatch it again and the device would actuate twice", terminal, got.Status)
			}
		})
	}

	// The counterweight: the guard must not be so tight that a genuine release fails.
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	id := seedWithStatus(t, api, ctx, "genuinely-claimed", CommandSent)
	if released, err := api.ReleaseClaim(ctx, id); err != nil || !released {
		t.Fatalf("a genuinely claimed SENT row must be releasable (released=%v err=%v)", released, err)
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

	seedWithStatus(t, api, ctx, "claim-me", CommandHeld)

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
	for _, from := range []CommandStatus{CommandQueued, CommandHeld, CommandParked} {
		t.Run(from.String(), func(t *testing.T) {
			api := newTestApi(t)
			ctx := core.WithTenant(context.Background(), "A")

			seedWithStatus(t, api, ctx, "cancel-"+from.String(), from)

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

// TestCancelLeavesASentCommandAlone is the counterweight to the test above, and the pair is
// the whole contract: a cancel takes what the platform still HOLDS and refuses what it has
// already handed over.
//
// 🔴 IT PINS A BEHAVIOUR THAT WAS PREVIOUSLY THE OPPOSITE, AND SILENTLY SO. CancelCommand
// guarded with "status NOT IN (terminal)", so SENT fell through by default rather than by
// decision. Cancelling a sent command stops no actuation — the device already has it — and
// CANCELLED is terminal, so MarkResponse then DISCARDS the answer the device sends back. The
// fleet acts, the responses vanish, and the record says an operator called it off. That is
// asserted below rather than merely described: the response must still land.
func TestCancelLeavesASentCommandAlone(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedWithStatus(t, api, ctx, "cancel-sent", CommandSent)

	got, err := api.CancelCommand(ctx, "cancel-sent")
	if err != nil {
		t.Fatalf("cancelling a SENT command must not error, it must no-op: %v", err)
	}
	if got.Status != CommandSent.String() {
		t.Fatalf("cancelling a SENT command recorded %s, want it left SENT", got.Status)
	}

	// The point of leaving it alone: the device's real answer still has somewhere to land.
	answered, err := api.MarkResponse(ctx, "cancel-sent", "d", true, nil, nil)
	if err != nil {
		t.Fatalf("MarkResponse failed: %v", err)
	}
	if answered.Status != CommandSuccessful.String() {
		t.Fatalf("a device answered a command a cancel had passed over and it recorded %s, want SUCCESSFUL — "+
			"if the cancel had taken it to CANCELLED, this answer would have been dropped", answered.Status)
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

	seedWithStatus(t, api, ctx, "p-queued", CommandQueued)
	seedWithStatus(t, api, ctx, "p-held", CommandHeld)
	seedWithStatus(t, api, ctx, "p-sent", CommandSent)
	seedWithStatus(t, api, ctx, "p-cancelled", CommandCancelled)
	seedWithStatus(t, api, ctx, "p-successful", CommandSuccessful)

	pending, err := api.PendingCommands(sysctx)
	if err != nil {
		t.Fatalf("PendingCommands failed: %v", err)
	}
	got := make(map[string]bool, len(pending))
	for _, c := range pending {
		got[c.Token] = true
	}

	if !got["p-queued"] {
		t.Fatalf("p-queued must be delivered by the sweep but PendingCommands omitted it (got %v)", got)
	}
	// p-held is in the UNWANTED list on purpose: the gate's whole value is that the sweep
	// stops polling held rows. If this ever passes with p-held present, the sweep is back
	// to loading an absent fleet's multi-day backlog on every tick.
	for _, unwanted := range []string{"p-held", "p-sent", "p-cancelled", "p-successful"} {
		if got[unwanted] {
			t.Fatalf("%s must NOT be selected by the sweep but PendingCommands returned it (got %v)", unwanted, got)
		}
	}
}

// TestCommandsFilterByStatuses covers the multi-status search filter the LwM2M
// wake drain needs (it asks for HELD ∪ PARKED in one query), including the empty
// list, which is deliberately "no filter" rather than "match nothing".
func TestCommandsFilterByStatuses(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedWithStatus(t, api, ctx, "f-queued", CommandQueued)
	seedWithStatus(t, api, ctx, "f-held", CommandHeld)
	seedWithStatus(t, api, ctx, "f-sent", CommandSent)
	seedWithStatus(t, api, ctx, "f-parked", CommandParked)
	seedWithStatus(t, api, ctx, "f-successful", CommandSuccessful)

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

	// The drain's actual query: the two states a waking device's backlog lives in — the
	// ones in which the PLATFORM still holds the command. SENT is deliberately not among
	// them; the fetcher's own wire test pins the same pair from the other side.
	drain := []string{CommandHeld.String(), CommandParked.String()}
	got := search(CommandSearchCriteria{Statuses: &drain})
	if !got["f-held"] || !got["f-parked"] {
		t.Fatalf("statuses filter must return both HELD and PARKED rows, got %v", got)
	}
	if got["f-queued"] || got["f-successful"] || got["f-sent"] {
		t.Fatalf("statuses filter returned rows outside the requested set: %v — a SENT row here\n"+
			"would mean the drain re-dispatching a command the device already has", got)
	}

	// An empty list is "no filter". Honouring it literally would make the result
	// depend on how the driver renders an empty IN rather than on the query.
	empty := []string{}
	got = search(CommandSearchCriteria{Statuses: &empty})
	if !got["f-queued"] || !got["f-held"] || !got["f-sent"] || !got["f-parked"] || !got["f-successful"] {
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
