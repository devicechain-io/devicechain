// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/presence"
)

// demotionWriter stands in for the durable stream, keeping every message so a test can
// decode what was actually published. It fakes TRANSPORT and nothing else — the payload
// it captures went through the real marshaller, so every field the emitter has to set is
// asserted on the wire rather than on a struct the test helped build.
type demotionWriter struct {
	msgs []messaging.Message
	// failAfter, when positive, makes the (failAfter+1)-th write fail. It exists for the
	// partial-failure case, which is the one place the walk's error handling matters.
	failAfter int
	err       error
}

func (w *demotionWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	if w.failAfter > 0 && len(w.msgs) >= w.failAfter {
		w.err = errors.New("nats: no responders available for subject devicechain.acme.inbound-events")
		return w.err
	}
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *demotionWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	panic("a demotion is tenant-shaped and must never take the per-device subject")
}

func (w *demotionWriter) HandleResponse(error) {}

// demotionFixedNow is the clock every test below runs on. It is well after the seeded
// presence stamps, which is what makes a demotion ordered.
var demotionFixedNow = time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)

// newDemotionApi builds the real projection over in-memory sqlite with a capturing
// writer wired in, and returns both plus a tenant-scoped context.
func newDemotionApi(t *testing.T) (*Api, *demotionWriter, context.Context) {
	t.Helper()
	api := newTestApi(t)
	w := &demotionWriter{}
	api.SetDemotionEmitter(NewDemotionEmitter(w, func() time.Time { return demotionFixedNow }))
	return api, w, core.WithTenant(context.Background(), "acme")
}

// seedAssertedSource writes n ASSERTED rows for one source through the real merge path,
// so every row has the shape a live transport would have left.
func seedAssertedSource(t *testing.T, api *Api, ctx context.Context, source string, n int) {
	t.Helper()
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	for i := range n {
		token := fmt.Sprintf("%s-dev-%03d", source, i)
		if _, err := api.MergeDeviceState(ctx, token, at, &PresenceTransition{
			Claim: presence.ClaimConnected, SessionId: uint64(i + 1), OccurredAt: at,
		}, DeviceIdentity{Source: source}); err != nil {
			t.Fatalf("seeding %s: %v", token, err)
		}
	}
}

// decodeDemotions decodes every captured message back off the wire.
func decodeDemotions(t *testing.T, w *demotionWriter) []*esmodel.UnresolvedEvent {
	t.Helper()
	out := make([]*esmodel.UnresolvedEvent, 0, len(w.msgs))
	for i, m := range w.msgs {
		ev, err := esproto.UnmarshalUnresolvedEvent(m.Value)
		if err != nil {
			t.Fatalf("message %d does not decode: %v", i, err)
		}
		out = append(out, ev)
	}
	return out
}

// TestTheEmittedDemotionCarriesTheArgumentSourceNotThisService is the field mapping,
// asserted on the DECODED EVENT rather than on a log line or the struct the test handed
// in — every one of these fields has to survive the marshaller to mean anything.
//
// 🔴 THE SOURCE IS THE ARGUMENT, AND "device-state" WOULD HAVE BEEN THE OBVIOUS WRONG
// ANSWER. MergeDeviceState refreshes device_states.source from every resolved event that
// carries one, so a demotion emitted under this service's own name rewrites the
// provenance of every row it touches. The fleet would come out of the repair filed under
// a source with no transport, no command path and no reconciler — strictly worse than the
// frozen state the demotion was run to fix, and invisible until somebody asked which
// transport a device was on.
func TestTheEmittedDemotionCarriesTheArgumentSourceNotThisService(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 1)

	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops@acme.test", "broker retired")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if result.Demoted != 1 || result.Scanned != 1 || result.Skipped != 0 {
		t.Fatalf("scanned/demoted/skipped = %d/%d/%d, want 1/1/0", result.Scanned, result.Demoted, result.Skipped)
	}

	events := decodeDemotions(t, w)
	if len(events) != 1 {
		t.Fatalf("published %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Source != "mqtt1" {
		t.Errorf("Source = %q, want the ARGUMENT %q — device_states.source is refreshed from this field",
			ev.Source, "mqtt1")
	}
	if ev.Device != "mqtt1-dev-000" {
		t.Errorf("Device = %q, want mqtt1-dev-000", ev.Device)
	}
	if ev.EventType != esmodel.StateChange {
		t.Errorf("EventType = %v, want StateChange", ev.EventType)
	}
	if !ev.AuthenticatedTransport {
		t.Error("AuthenticatedTransport is false: under deviceAuthMode=required the resolver " +
			"would demand a per-device credential this platform-minted event does not carry")
	}
	if !ev.OccurredTime.Equal(demotionFixedNow) {
		t.Errorf("OccurredTime = %v, want the demotion's own instant %v", ev.OccurredTime, demotionFixedNow)
	}
	payload, ok := ev.Payload.(*esmodel.UnresolvedStateChangePayload)
	if !ok {
		t.Fatalf("payload is %T, want *UnresolvedStateChangePayload", ev.Payload)
	}
	if payload.State != esmodel.PresenceDemoted {
		t.Errorf("State = %q, want DEMOTED", payload.State)
	}
	// The row was seeded on session 1; the demotion must name the session the row HOLDS.
	if payload.SessionId != "1" {
		t.Errorf("SessionId = %q, want the row's stored session %q — a demotion applies only "+
			"against the session it releases, so any other value is a permanent silent no-op",
			payload.SessionId, "1")
	}
	if payload.ExpectedSessionId != "" {
		t.Errorf("ExpectedSessionId = %q, want empty — the resolver REFUSES a demotion that sets it",
			payload.ExpectedSessionId)
	}
	if payload.Reason != "operator-demotion(actor=ops@acme.test): broker retired" {
		t.Errorf("Reason = %q, want the actor-prefixed form", payload.Reason)
	}
	if w.msgs[0].DedupID == "" {
		t.Error("no dedup id: two replicas answering the same operator call would publish twice")
	}
	if string(w.msgs[0].Key) != "mqtt1-dev-000" {
		t.Errorf("message key = %q, want the device token", string(w.msgs[0].Key))
	}
}

// TestAnEmptyDeviceTokenListDemotesNothing is the divergence from
// CommandSearchCriteria.Statuses, and it is the whole reason the three states are carried
// as a pointer.
//
// 🔴 THE TWO READINGS ARE BOTH DEFENSIBLE AND THEY ARE OPPOSITES HERE. For a read filter,
// "I built a list and it came out empty" almost always means "no preference", and the
// other reading turns that into a silently empty page. For a write whose unnarrowed blast
// radius is an entire event source's fleet, the same slip has to fail in the direction
// that does NOTHING. Collapsing the two states — which a plain []string would do — makes
// `deviceTokens: []` demote the whole source.
func TestAnEmptyDeviceTokenListDemotesNothing(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 5)

	empty := []string{}
	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", &empty, 0, 10, "ops", "typo guard")
	if err != nil {
		t.Fatalf("an empty list is a legitimate request that demotes nothing, not an error: %v", err)
	}
	if result.Scanned != 0 || result.Demoted != 0 || result.LastId != 0 {
		t.Fatalf("an empty deviceTokens list scanned %d and demoted %d (lastId %d), want 0/0/0 — "+
			"it collapsed onto \"no narrowing\" and took the whole source",
			result.Scanned, result.Demoted, result.LastId)
	}
	if len(w.msgs) != 0 {
		t.Fatalf("%d demotion(s) were published for an empty device list", len(w.msgs))
	}

	// The counterweight, without which the above is satisfied by an implementation that
	// demotes nothing at all: OMITTED means the whole source.
	omitted, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "whole source")
	if err != nil {
		t.Fatalf("omitted deviceTokens: %v", err)
	}
	if omitted.Demoted != 5 {
		t.Fatalf("an omitted deviceTokens list demoted %d of 5 — omitted and empty must not be the same request",
			omitted.Demoted)
	}
}

// TestDeviceTokensNarrowWithinTheSourceAndNeverPastIt pins the AND. Written as an OR the
// named devices would be demoted whatever source they belong to, which turns a targeted
// repair into a cross-source write nobody asked for.
func TestDeviceTokensNarrowWithinTheSourceAndNeverPastIt(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 3)
	seedAssertedSource(t, api, ctx, "lwm2m", 3)

	// One device of the named source, and one belonging to a DIFFERENT source.
	tokens := []string{"mqtt1-dev-001", "lwm2m-dev-002"}
	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", &tokens, 0, 10, "ops", "one machine")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if result.Demoted != 1 {
		t.Fatalf("demoted %d, want exactly 1 — the token list reached past the source it was scoped to",
			result.Demoted)
	}
	events := decodeDemotions(t, w)
	if events[0].Device != "mqtt1-dev-001" {
		t.Fatalf("demoted %q, want mqtt1-dev-001", events[0].Device)
	}
	if events[0].Source != "mqtt1" {
		t.Fatalf("Source = %q, want mqtt1", events[0].Source)
	}
}

// TestTheKeysetWalkCoversEveryRowOnceWithRowsInsertedMidWalk is why the cursor is a row
// id and not an offset.
//
// 🔑 THE INSERTS ARE THE TEST. An OFFSET page skips one row for every row written before
// the cursor between pages, and a device silently not demoted is indistinguishable from a
// device that did not need it — the walk finishes, reports a total, and leaves part of the
// fleet frozen. Devices connect underneath a fleet-wide repair by definition, so this is
// the normal case rather than a corner one.
func TestTheKeysetWalkCoversEveryRowOnceWithRowsInsertedMidWalk(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 20)

	const limit = 5
	seen := map[string]bool{}
	var after uint64
	inserted := 0
	for page := 0; page < 30; page++ {
		result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, after, limit, "ops", "walk")
		if err != nil {
			t.Fatalf("page after %d: %v", after, err)
		}
		if result.Scanned == 0 {
			break
		}
		after = result.LastId
		// A new device connects to the same source mid-walk, landing at a HIGHER id than
		// the cursor. It must be demoted by a later page, exactly once.
		if inserted < 3 {
			token := fmt.Sprintf("mqtt1-late-%03d", inserted)
			at := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)
			if _, err := api.MergeDeviceState(ctx, token, at, &PresenceTransition{
				Claim: presence.ClaimConnected, SessionId: uint64(900 + inserted), OccurredAt: at,
			}, DeviceIdentity{Source: "mqtt1"}); err != nil {
				t.Fatalf("mid-walk insert: %v", err)
			}
			inserted++
		}
		if result.Scanned < limit {
			break
		}
	}

	for _, ev := range decodeDemotions(t, w) {
		if seen[ev.Device] {
			t.Fatalf("device %s was demoted twice; the cursor is not advancing", ev.Device)
		}
		seen[ev.Device] = true
	}
	if len(seen) != 23 {
		t.Fatalf("the walk demoted %d of 23 devices (20 seeded + 3 inserted mid-walk); "+
			"a keyset cursor covers every row exactly once", len(seen))
	}
}

// applyDemotions stands in for the pipeline catching up: it flips to INFERRED every row
// this walk has already demoted. It exists because a demotion is an EVENT, not a write —
// the mutation publishes, the resolver resolves, and MergeDeviceState moves the row some
// milliseconds later, WHILE THE WALK IS STILL GOING. So the set being walked shrinks
// underneath it, which is the scenario the two tests below are about.
func applyDemotions(t *testing.T, api *Api, ctx context.Context, w *demotionWriter) {
	t.Helper()
	for _, ev := range decodeDemotions(t, w) {
		if err := api.RDB.DB(ctx).Model(&DeviceState{}).
			Where("device_token = ?", ev.Device).
			Update("presence_source", PresenceSourceInferred).Error; err != nil {
			t.Fatalf("applying the demotion for %s: %v", ev.Device, err)
		}
	}
}

// TestTheWalkStaysCorrectWhileTheSetShrinksUnderneathIt is the case an OFFSET cursor
// cannot survive, and it is the NORMAL case rather than a corner one.
//
// 🔴 A DEMOTION IS AN EVENT, SO THE PIPELINE APPLIES IT MID-WALK. Each page's rows leave
// the ASSERTED set a moment after that page returns, so by page two the filtered set has
// lost its front. A keyset cursor is unaffected — it names a row id, and the rows after
// that id are the same rows whatever happened before it. An OFFSET of the same magnitude
// now steps over rows that moved UP into the positions it skips: with 20 rows and a limit
// of 5, page two under OFFSET returns ids 11–15 and ids 6–10 are never visited by any
// page. They are not reported missing; the walk terminates normally, reports a total, and
// leaves a third of the fleet frozen.
//
// The mid-walk INSERT case (a device connecting during the repair) is covered by
// TestTheKeysetWalkCoversEveryRowOnceWithRowsInsertedMidWalk. It is the weaker of the two:
// an insert lands at a HIGHER id than the cursor, so an OFFSET walk survives it. Only the
// shrink separates the two cursors.
func TestTheWalkStaysCorrectWhileTheSetShrinksUnderneathIt(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 20)

	const limit = 5
	seen := map[string]bool{}
	var after uint64
	for page := 0; page < 30; page++ {
		result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, after, limit, "ops", "walk")
		if err != nil {
			t.Fatalf("page after %d: %v", after, err)
		}
		for _, ev := range decodeDemotions(t, w) {
			seen[ev.Device] = true
		}
		// The pipeline catches up: everything demoted so far leaves the ASSERTED set.
		applyDemotions(t, api, ctx, w)
		if result.Scanned < limit {
			break
		}
		after = result.LastId
	}
	if len(seen) != 20 {
		missing := make([]string, 0)
		for i := range 20 {
			token := fmt.Sprintf("mqtt1-dev-%03d", i)
			if !seen[token] {
				missing = append(missing, token)
			}
		}
		t.Fatalf("the walk demoted %d of 20 devices; %v were never visited by any page — "+
			"the cursor stepped over rows that left the set while the walk was running",
			len(seen), missing)
	}
}

// TestTheWalkEmptiesItselfAndDoesNotReDemoteAnInferredRow is the self-emptying property,
// and it is what makes a repeated repair safe to run.
//
// 🔴 AN ALREADY-DEMOTED ROW PASSES EVERY SKIP CONDITION. It keeps its session (deliberately
// — that is what a re-assertion must beat), it keeps a presence time, and that time is in
// the past. So nothing downstream of the query would refuse it: without the
// `presence_source = ASSERTED` predicate at the SCAN, a second pass re-demotes every row
// it demoted on the first, forever, on every pass, publishing a fleet-sized burst of
// events that can only be dropped by the ordering guard at the far end.
func TestTheWalkEmptiesItselfAndDoesNotReDemoteAnInferredRow(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 6)

	first, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 100, "ops", "first pass")
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.Demoted != 6 {
		t.Fatalf("first pass demoted %d of 6", first.Demoted)
	}
	applyDemotions(t, api, ctx, w)
	before := len(w.msgs)

	// A second pass from the beginning. The set is now empty, so there is nothing to do.
	second, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 100, "ops", "second pass")
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Scanned != 0 {
		t.Fatalf("the second pass scanned %d rows over a source whose devices are all INFERRED; "+
			"the scan is not filtered on ASSERTED, so this repair never empties and re-publishes "+
			"a fleet-sized burst on every pass", second.Scanned)
	}
	if len(w.msgs) != before {
		t.Fatalf("the second pass published %d more event(s) for rows that are already INFERRED",
			len(w.msgs)-before)
	}

	// The counterweight: a row that is STILL asserted is still found, so the emptiness
	// above is emptiness and not blindness.
	seedAssertedSource(t, api, ctx, "mqtt1", 7) // re-seeds 000..005 (no-ops) plus a 7th
	third, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 100, "ops", "third pass")
	if err != nil {
		t.Fatalf("third pass: %v", err)
	}
	if third.Demoted != 1 {
		t.Fatalf("the third pass demoted %d, want exactly the 1 row that is still ASSERTED", third.Demoted)
	}
}

// TestTheWalkAdvancesPastARowItCannotDemote is the other half of the cursor rule: LastId
// is the last row SCANNED, not the last one kept. Resuming from the last row DEMOTED
// would re-read every skipped row on the next page, and a permanently-skippable row —
// which all three skip conditions are — would make the walk never terminate.
func TestTheWalkAdvancesPastARowItCannotDemote(t *testing.T) {
	api, _, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 3)
	// Make the LAST row of the page one this pass cannot release, so the cursor is forced
	// to prove it tracks the last row SCANNED. A presence time in the future is the live
	// skip condition — a producer whose clock leads the platform's refuses every demotion
	// we can mint until ours catches up, which is precisely the row that would be re-read
	// forever by a cursor tracking kept rows.
	if err := api.RDB.DB(ctx).Model(&DeviceState{}).
		Where("device_token = ?", "mqtt1-dev-002").
		Update("presence_time", demotionFixedNow.Add(time.Hour)).Error; err != nil {
		t.Fatalf("seeding the skippable row: %v", err)
	}

	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "walk")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if result.Scanned != 3 || result.Demoted != 2 || result.Skipped != 1 {
		t.Fatalf("scanned/demoted/skipped = %d/%d/%d, want 3/2/1",
			result.Scanned, result.Demoted, result.Skipped)
	}
	var skipped DeviceState
	if err := api.RDB.DB(ctx).Where("device_token = ?", "mqtt1-dev-002").First(&skipped).Error; err != nil {
		t.Fatalf("reading the skipped row: %v", err)
	}
	if result.LastId != uint64(skipped.ID) {
		t.Fatalf("lastId = %d, want the last row SCANNED (%d) — a cursor tracking the last row KEPT "+
			"re-reads the skipped row on every subsequent page and the walk never ends",
			result.LastId, skipped.ID)
	}
	// The next page is genuinely empty, which is what termination looks like.
	next, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, result.LastId, 10, "ops", "walk")
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if next.Scanned != 0 {
		t.Fatalf("the page after the cursor scanned %d rows, want 0", next.Scanned)
	}
}

// TestARowThatCannotBeReleasedIsSkippedAndCounted covers both conditions under which an
// emitted demotion could not have applied.
//
// 🔑 EACH IS A SILENT PERMANENT NO-OP IF EMITTED. A demotion against a row with no
// presence time is dropped by the ordering guard, indistinguishable from a stale echo; a
// demotion whose stamp is not after the row's is refused by acceptsDemotion's strict
// After. Counting them is what turns "demoted: 40" over a fleet of 60 into a question the
// operator can ask.
func TestARowThatCannotBeReleasedIsSkippedAndCounted(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(t *testing.T, api *Api, ctx context.Context)
	}{
		{"no presence time — the ordering guard has nothing to judge it against", func(t *testing.T, api *Api, ctx context.Context) {
			if err := api.RDB.DB(ctx).Model(&DeviceState{}).
				Where("device_token = ?", "mqtt1-dev-000").Update("presence_time", nil).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"a presence time that is not in the past — a demotion stamped now is stale", func(t *testing.T, api *Api, ctx context.Context) {
			if err := api.RDB.DB(ctx).Model(&DeviceState{}).
				Where("device_token = ?", "mqtt1-dev-000").
				Update("presence_time", demotionFixedNow.Add(time.Hour)).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			api, w, ctx := newDemotionApi(t)
			seedAssertedSource(t, api, ctx, "mqtt1", 1)
			c.mutate(t, api, ctx)

			result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "repair")
			if err != nil {
				t.Fatalf("a row that cannot be released is skipped, not an error: %v", err)
			}
			if result.Scanned != 1 || result.Skipped != 1 || result.Demoted != 0 {
				t.Fatalf("scanned/demoted/skipped = %d/%d/%d, want 1/0/1",
					result.Scanned, result.Demoted, result.Skipped)
			}
			if len(w.msgs) != 0 {
				t.Fatalf("%d event(s) published for a row the pipeline would have refused", len(w.msgs))
			}
		})
	}

	// The counterweight: an ordinary row is NOT skipped, so the three cases above are
	// not satisfied by a walker that skips everything.
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 1)
	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "repair")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if result.Demoted != 1 || result.Skipped != 0 {
		t.Fatalf("an ordinary asserted row was skipped: demoted/skipped = %d/%d, want 1/0",
			result.Demoted, result.Skipped)
	}
	if len(w.msgs) != 1 {
		t.Fatalf("published %d events for one demotable row", len(w.msgs))
	}
}

// TestTwoIdenticalCallsCollapseOntoOneDedupId. The mutation is a RE-DERIVATION — "release
// session S of device D" — whose truth does not depend on when it was said, so a retry or
// two replicas answering one operator click must produce ONE event. A per-call nonce would
// break that, which is precisely the right choice for the boundary drain and the wrong one
// here.
func TestTwoIdenticalCallsCollapseOntoOneDedupId(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 1)

	for i := range 2 {
		if _, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "retry"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(w.msgs) != 2 {
		t.Fatalf("captured %d messages, want 2 (the fake writer does not dedup — JetStream does)", len(w.msgs))
	}
	if w.msgs[0].DedupID != w.msgs[1].DedupID {
		t.Fatalf("two identical demotions minted different dedup ids (%q vs %q); the broker would "+
			"store both and the second is a no-op event on the shared stream",
			w.msgs[0].DedupID, w.msgs[1].DedupID)
	}

	// A DIFFERENT session is a different release and must stay distinct, or a device that
	// reconnected and was demoted again would be suppressed by the earlier entry.
	moved := demotionDedupID("acme", "mqtt1-dev-000", 2)
	if moved == w.msgs[0].DedupID {
		t.Fatal("a demotion of a different session produced the same dedup id")
	}
	// And so is a different TENANT: the dedup window is stream-scoped, so an id that is
	// not tenant-scoped makes one tenant's demotion suppress another's.
	if demotionDedupID("other", "mqtt1-dev-000", 1) == w.msgs[0].DedupID {
		t.Fatal("two tenants' demotions of the same device token produced the same dedup id")
	}
}

// TestADemotionRefusesArgumentsItCannotActOn. Each of these is refused rather than
// defaulted, and the reason is the same in every case: the quiet alternative produces a
// call that looks like it worked.
func TestADemotionRefusesArgumentsItCannotActOn(t *testing.T) {
	for _, c := range []struct {
		name   string
		source string
		limit  int
		reason string
		want   string
	}{
		{"limit 0 — GraphQL supplies a zero for an Int! nobody bound, and zero-means-unlimited " +
			"would restore the unbounded write the page bound exists to remove", "mqtt1", 0, "why", "limit"},
		{"limit above the page ceiling", "mqtt1", MaxAssertedPageSize + 1, "why", "limit"},
		{"a negative limit", "mqtt1", -1, "why", "limit"},
		{"a blank source — the blast radius is a whole source, so it is never inferred", "  ", 10, "why", "source"},
		{"a blank reason — String! is satisfied by \"\", and the only record of a fleet-wide " +
			"write would then say nothing", "mqtt1", 10, "   ", "reason"},
	} {
		t.Run(c.name, func(t *testing.T) {
			api, w, ctx := newDemotionApi(t)
			seedAssertedSource(t, api, ctx, "mqtt1", 2)
			_, err := api.DemoteAssertedPresence(ctx, c.source, nil, 0, c.limit, "ops", c.reason)
			if err == nil {
				t.Fatalf("the call was accepted; %d event(s) published", len(w.msgs))
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("the refusal must name the offending argument %q, got %v", c.want, err)
			}
			if len(w.msgs) != 0 {
				t.Fatalf("%d event(s) published by a refused call", len(w.msgs))
			}
		})
	}
}

// TestADemotionFailsClosedWithNoWriter. A service that came up without its messaging
// components must refuse rather than report a successful demotion of zero devices — the
// second is a green answer to a question that was never asked.
func TestADemotionFailsClosedWithNoWriter(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	seedAssertedSource(t, api, ctx, "mqtt1", 2)

	_, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "repair")
	if !errors.Is(err, ErrNoDemotionEmitter) {
		t.Fatalf("a demotion with no writer returned %v, want ErrNoDemotionEmitter", err)
	}
	if !errors.Is(err, ErrDemotionPublish) {
		t.Fatal("ErrNoDemotionEmitter must wrap ErrDemotionPublish so the resolver sanitizes it " +
			"rather than telling a tenant client which internal component is missing")
	}
}

// TestAFailedPublishAbandonsThePageRatherThanReportingPartialSuccess. A count that
// included rows whose write failed would tell the caller they were demoted, and the
// caller's next page starts after them — so the failure would be both invisible and never
// retried. The identical call is idempotent at the dedup window, so abandoning is safe.
func TestAFailedPublishAbandonsThePageRatherThanReportingPartialSuccess(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 5)
	w.failAfter = 2 // the third write fails

	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "repair")
	if err == nil {
		t.Fatalf("a failed publish was reported as success: %+v", result)
	}
	if result != nil {
		t.Fatalf("a partial result was returned alongside the error: %+v", result)
	}
	if !errors.Is(err, ErrDemotionPublish) {
		t.Fatalf("a publish failure must wrap ErrDemotionPublish so the resolver sanitizes it, got %v", err)
	}
	if !strings.Contains(err.Error(), "mqtt1-dev-002") {
		t.Errorf("the error must name the device it failed on, got %v", err)
	}
}

// TestAnEmitWithNoTenantIsRefused. The subject the message is published to and the tenant
// its dedup id is scoped by both come from context, so a call with no tenant is refused
// rather than published unscoped — and an unscoped dedup id is worse than the duplicate it
// prevents, since it lets one tenant's demotion suppress another's.
func TestAnEmitWithNoTenantIsRefused(t *testing.T) {
	w := &demotionWriter{}
	e := NewDemotionEmitter(w, func() time.Time { return demotionFixedNow })
	err := e.EmitDemotion(context.Background(), "mqtt1", "dev-1", 7, demotionFixedNow, "r")
	if !errors.Is(err, core.ErrNoTenant) {
		t.Fatalf("EmitDemotion with no tenant returned %v, want core.ErrNoTenant", err)
	}
	if len(w.msgs) != 0 {
		t.Fatal("a message was published with no tenant in context")
	}
}

// TestASessionlessRowIsDemotedRatherThanSkipped guards a rule that was written into this
// door and had to be taken back out. A demotion applies against the session the row already
// holds — and a producer may legitimately send no session id, so an asserted row can hold
// ZERO, which is exactly what releases it.
//
// 🔴 SKIPPING SUCH ROWS IS THE FAILURE THIS WHOLE DOOR EXISTS TO FIX, WEARING ITS UNIFORM.
// They would become the one population the operator can never reach: permanently frozen,
// and reported as "skipped" rather than as broken, so the count would look like the system
// working. It fails in the direction that reads as success, which is why it gets its own
// test rather than living in the skip table's counterweight.
func TestASessionlessRowIsDemotedRatherThanSkipped(t *testing.T) {
	api, w, ctx := newDemotionApi(t)
	seedAssertedSource(t, api, ctx, "mqtt1", 1)
	if err := api.RDB.DB(ctx).Model(&DeviceState{}).
		Where("device_token = ?", "mqtt1-dev-000").Update("session_id", 0).Error; err != nil {
		t.Fatalf("seeding the session-less row: %v", err)
	}

	result, err := api.DemoteAssertedPresence(ctx, "mqtt1", nil, 0, 10, "ops", "repair")
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if result.Scanned != 1 || result.Demoted != 1 || result.Skipped != 0 {
		t.Fatalf("scanned/demoted/skipped = %d/%d/%d, want 1/1/0 — a session-less row is "+
			"releasable, and skipping it strands the row forever",
			result.Scanned, result.Demoted, result.Skipped)
	}
	if len(w.msgs) != 1 {
		t.Fatalf("%d event(s) published, want 1", len(w.msgs))
	}
}
