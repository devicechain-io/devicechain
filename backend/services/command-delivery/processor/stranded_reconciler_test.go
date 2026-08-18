// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// lwm2mSource is what an LwM2M device is ACTUALLY projected with: lwm2m-ingest holds a
// bare compile-time constant that no config field can override, and it survives every hop
// to device_states.source unchanged.
//
// 🔴 IT IS BARE, NOT QUALIFIED, AND A FIXTURE WRITING "lwm2m:server-1" WOULD BE TESTING A
// VALUE NOTHING EMITS — the same fiction that made the Sparkplug deny list unreachable in
// production while its tests stayed green. See sparkplugSource for that story.
const lwm2mSource = "lwm2m"

// strandedCmd builds a command in the shape the scan returns them: SENT, aged, and
// carrying the dispatch nonce its claim stamped.
func strandedCmd(id uint, token, device, nonce string, age time.Duration) *model.Command {
	cmd := &model.Command{DeviceToken: device, Name: "reboot"}
	cmd.ID = id
	cmd.Token = token
	cmd.TenantId = "acme"
	cmd.Status = model.CommandSent.String()
	cmd.SentTime = sql.NullTime{Time: time.Now().Add(-age), Valid: true}
	cmd.DispatchNonce = sql.NullString{String: nonce, Valid: true}
	return cmd
}

// strandedProc wires a processor for the stranded pass: the fake API, a presence reader,
// and the lock held.
func strandedProc(api *fakeApi, reader presence.Reader) *CommandDeliveryProcessor {
	return &CommandDeliveryProcessor{Api: api, Presence: reader}
}

// TestStrandedParkQuotesTheNonceTheScanObserved is THE test of this pass, and the
// assertion is on the nonce rather than on the token.
//
// 🔴🔴 A TEST THAT ONLY CHECKED WHICH TOKENS WERE PARKED WOULD PASS AGAINST A PARK
// PREDICATED ON NOTHING AT ALL. The nonce is what makes the write name the dispatch this
// pass actually saw, and dropping it re-opens the re-arm hole inside a single pass: a late
// park retires the claim without clearing the nonce, the device wakes, the drain claims
// with a FRESH nonce and ACTUATES, and a park predicated only on status = 'SENT' would
// match that freshly-actuated row and arm it for a second physical actuation.
//
// The model-level half — that the database write genuinely refuses a stale nonce — is
// TestParkClaimRefusesAStaleNonce. This half is that the reconciler HANDS IT the right
// one; both are needed, and neither implies the other.
func TestStrandedParkQuotesTheNonceTheScanObserved(t *testing.T) {
	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "nonce-abc", time.Hour)},
	}
	reader := &scriptedReader{states: map[string]presence.State{
		"dev-1": asserted(false, lwm2mSource),
	}}

	strandedProc(api, reader).reconcileStranded(context.Background())

	if len(api.parkCalls) != 1 {
		t.Fatalf("expected exactly one park, got %d (%+v)", len(api.parkCalls), api.parkCalls)
	}
	if api.parkCalls[0].token != "c1" {
		t.Fatalf("parked %q, want c1", api.parkCalls[0].token)
	}
	if api.parkCalls[0].nonce != "nonce-abc" {
		t.Fatalf("park quoted nonce %q, want the one the scan observed (nonce-abc). A park that "+
			"does not name the dispatch it saw can retire a NEWER one, which re-arms a command "+
			"the device has already been given", api.parkCalls[0].nonce)
	}
}

// TestStrandedSkipsEveryTransportButLwM2M is the allow list, and it is asserted as an
// allow list rather than as "MQTT is excluded".
//
// 🔴 THE POLARITY IS THE SAFETY ARGUMENT. Every other transport gate here is a DENY list,
// where an unclassified source falls through to the permissive answer. If someone
// "harmonized" this one into that shape, an unrecognized source would start being parked —
// and the unrecognized case is the operator-chosen MQTT event-source id, i.e. the common
// one. So the unknown source below must be REFUSED, and a test that only listed the known
// transports would not notice if it stopped being.
func TestStrandedSkipsEveryTransportButLwM2M(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
	}{
		{"mqtt", mqttSource},
		{"sparkplug", sparkplugSource},
		{"an operator's own namespace", "acme:line3"},
		{"a source nothing has classified", "some-new-transport"},
		{"no source at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeApi{
				strandedLockAvailable: true,
				strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "n1", time.Hour)},
			}
			reader := &scriptedReader{states: map[string]presence.State{
				"dev-1": asserted(false, tc.source),
			}}

			strandedProc(api, reader).reconcileStranded(context.Background())

			if len(api.parkCalls) != 0 {
				t.Fatalf("parked a command on source %q; this pass may only act where the platform "+
					"can know the command reached nothing", tc.source)
			}
		})
	}
}

// TestStrandedActsOnAQualifiedLwM2MSource: classification cuts at the FIRST colon, so a
// qualified LwM2M source is still LwM2M. Paired with the case above, this is what proves
// the gate classifies rather than string-matches.
func TestStrandedActsOnAQualifiedLwM2MSource(t *testing.T) {
	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "n1", time.Hour)},
	}
	reader := &scriptedReader{states: map[string]presence.State{
		"dev-1": asserted(false, "lwm2m:site-a"),
	}}

	strandedProc(api, reader).reconcileStranded(context.Background())

	if len(api.parkCalls) != 1 {
		t.Fatalf("a qualified LwM2M source was not acted on; got %d parks", len(api.parkCalls))
	}
}

// TestStrandedDoesNothingWithoutTheLock: N replicas must not all walk the same set.
func TestStrandedDoesNothingWithoutTheLock(t *testing.T) {
	api := &fakeApi{
		strandedLockAvailable: false,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "n1", time.Hour)},
	}
	reader := &scriptedReader{states: map[string]presence.State{"dev-1": asserted(false, lwm2mSource)}}

	strandedProc(api, reader).reconcileStranded(context.Background())

	if api.strandedLockAttempts != 1 {
		t.Fatalf("expected one lock attempt, got %d", api.strandedLockAttempts)
	}
	// Not merely "parked nothing" — it must not have READ either. A pass that does the
	// scan and skips only the write still burns the reads on every replica.
	if len(api.strandedReads) != 0 {
		t.Fatalf("a replica without the lock must not scan at all, got %d reads", len(api.strandedReads))
	}
	if len(api.parkCalls) != 0 {
		t.Fatalf("a replica without the lock parked %d commands", len(api.parkCalls))
	}
}

// TestStrandedDoesNothingWithoutAPresenceReader is the fail-CLOSED direction, and it is
// the opposite of the sweep's fail-open on the same missing dependency.
//
// With no reader there is no source, so there is no transport, so the allow list has
// nothing to allow on. Acting anyway would park every stranded command regardless of
// transport — including MQTT, where PARKED asserts something a QoS-0 publish cannot know.
func TestStrandedDoesNothingWithoutAPresenceReader(t *testing.T) {
	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "n1", time.Hour)},
	}

	strandedProc(api, nil).reconcileStranded(context.Background())

	if len(api.strandedReads) != 0 || len(api.parkCalls) != 0 {
		t.Fatalf("with no presence reader the pass must not scan or park; reads=%d parks=%d",
			len(api.strandedReads), len(api.parkCalls))
	}
}

// TestStrandedLeavesAloneWhatAFailedReadCouldNotCover: an absent entry after a FAILED read
// is not an answer, and treating it as one would park on an unknown transport.
//
// 🔴🔴 IT ASSERTS THE SKIP REASON, NOT MERELY THAT NOTHING WAS PARKED, AND THAT IS THE
// WHOLE TEST. A mutation harness proved why: deleting the Resolved guard entirely leaves
// this pass parking nothing anyway, because a device the read could not cover has an EMPTY
// source, and the transport allow list rejects an empty source too. So "parked nothing"
// cannot tell the two apart, and a test asserting only that passes against code with no
// presence guard at all.
//
// What actually differs is the ACCOUNT the pass gives of itself. With the guard, a
// device-state outage reads as skipped{reason="presence_unknown"}. Without it, the whole
// fleet is filed under skipped{reason="transport"} — a reading that says "these devices
// are on the wrong transport" when the truth is "we cannot see any of them". That counter
// exists precisely so an operator can tell a broken projection from a working exclusion,
// and it is the only thing here that can.
func TestStrandedLeavesAloneWhatAFailedReadCouldNotCover(t *testing.T) {
	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "n1", time.Hour)},
	}
	reader := &scriptedReader{err: errors.New("device-state unreachable")}
	proc := strandedProc(api, reader)
	proc.StrandedSkipped = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "test_stranded_skipped_total"}, []string{"reason"})

	proc.reconcileStranded(context.Background())

	if len(api.parkCalls) != 0 {
		t.Fatalf("parked %d commands on a failed presence read; the transport was unknown",
			len(api.parkCalls))
	}
	if got := testutil.ToFloat64(proc.StrandedSkipped.WithLabelValues(skipPresenceUnknown)); got != 1 {
		t.Fatalf("skipped{reason=%q} = %v, want 1. A device the presence read could not cover "+
			"must be reported as unseen, not as excluded by transport — those send an operator "+
			"to two different problems", skipPresenceUnknown, got)
	}
	if got := testutil.ToFloat64(proc.StrandedSkipped.WithLabelValues(skipTransport)); got != 0 {
		t.Fatalf("skipped{reason=%q} = %v, want 0; a presence outage was filed as a transport "+
			"exclusion", skipTransport, got)
	}
}

// TestStrandedSkipsARowWithNoNonce: without a nonce there is no way to name the dispatch
// the scan observed, so there is no safe park to issue.
func TestStrandedSkipsARowWithNoNonce(t *testing.T) {
	cmd := strandedCmd(1, "c1", "dev-1", "", time.Hour)
	cmd.DispatchNonce = sql.NullString{}
	api := &fakeApi{strandedLockAvailable: true, strandedPage: []*model.Command{cmd}}
	reader := &scriptedReader{states: map[string]presence.State{"dev-1": asserted(false, lwm2mSource)}}

	strandedProc(api, reader).reconcileStranded(context.Background())

	if len(api.parkCalls) != 0 {
		t.Fatalf("issued a park with no nonce to quote (%+v); such a write names no dispatch and "+
			"must not be attempted", api.parkCalls)
	}
}

// TestStrandedPassesTheHorizonItDerived checks the pass asks for rows older than
// StrandedSentGrace rather than for everything currently in SENT.
//
// 🔑 THE ASSERTION IS ON THE DERIVED VALUE, NOT ON A COPY OF IT. Comparing against a
// re-typed 330s would keep passing after MaxDeliver or AckWait changed, which is the exact
// silent drift deriving the constant exists to prevent.
func TestStrandedPassesTheHorizonItDerived(t *testing.T) {
	api := &fakeApi{strandedLockAvailable: true}
	reader := &scriptedReader{}

	before := time.Now()
	strandedProc(api, reader).reconcileStranded(context.Background())

	if len(api.strandedHorizons) != 1 {
		t.Fatalf("expected one scan, got %d", len(api.strandedHorizons))
	}
	asked := api.strandedHorizons[0]
	want := before.Add(-StrandedSentGrace)
	if asked.Before(want.Add(-time.Second)) || asked.After(want.Add(time.Minute)) {
		t.Fatalf("scanned for rows older than %v, want about %v (StrandedSentGrace = %v)",
			asked, want, StrandedSentGrace)
	}
	if StrandedSentGrace <= 0 {
		t.Fatal("StrandedSentGrace is not positive, so every SENT row is instantly eligible " +
			"and this pass races the delivery it is supposed to wait out")
	}
}

// TestStrandedCarriesItsCursorBetweenPasses is the processor's half of the cursor
// contract: whatever the scan answers must be handed back on the NEXT pass.
//
// 🔴 THE ROWS THIS PASS DECLINES STAY ELIGIBLE FOREVER, so it cannot rely on its result
// set shrinking to make progress. If the processor dropped the cursor on the floor —
// scanning from the start every time — an MQTT-heavy instance would re-read the same
// undeclinable first page on every tick and never reach the LwM2M rows behind it. The
// pass would run on schedule, observe work, and be a permanent no-op. The model's half
// (that the query honours the cursor) is TestScanCursorAdvancesPastRowsItCannotMove.
func TestStrandedCarriesItsCursorBetweenPasses(t *testing.T) {
	resume := model.StrandedCursor{SentTime: time.Now().Add(-time.Hour).Truncate(time.Second), ID: 77}
	api := &fakeApi{strandedLockAvailable: true, strandedForceNext: &resume}
	reader := &scriptedReader{}
	proc := strandedProc(api, reader)

	proc.reconcileStranded(context.Background())
	proc.reconcileStranded(context.Background())

	if len(api.strandedReads) != 2 {
		t.Fatalf("expected two scans, got %d", len(api.strandedReads))
	}
	if !api.strandedReads[0].AtStart() {
		t.Fatalf("the first pass resumed from %+v; with no prior state it must start at the "+
			"beginning", api.strandedReads[0])
	}
	if api.strandedReads[1] != resume {
		t.Fatalf("the second pass scanned from %+v, want the cursor the first pass was given "+
			"(%+v). Restarting the walk every tick strands every row behind the first page",
			api.strandedReads[1], resume)
	}
}

// TestConstructorWiresTheStrandedPass exists because every other test in this file builds
// the processor BY LITERAL.
//
// 🔴 THAT MEANS DELETING THE CONSTRUCTOR'S WIRING WOULD LEAVE THIS WHOLE SUITE GREEN. The
// literal-built processor tolerates nil counters by design and takes its API and reader as
// plain fields, so the constructor could stop wiring any of it and nothing above would
// notice. This asks the constructor itself.
//
// It DRIVES THE PASS rather than reading the fields back, following this file's rule: a
// field being set proves assignment, not that the path consults what was assigned. The
// counters are checked directly afterwards because they have no observable behaviour to
// drive — nil is tolerated everywhere by design.
//
// ⚠️ The functional area differs from the one TestConstructorWiresBothDeliveryGates uses,
// and it has to. The counters register into the process-wide default registry keyed by
// subsystem, so two constructor tests sharing an area would panic on duplicate
// registration rather than fail.
func TestConstructorWiresTheStrandedPass(t *testing.T) {
	ms := &core.Microservice{FunctionalArea: "commanddeliverystranded"}
	api := &fakeApi{
		strandedLockAvailable: true,
		strandedPage:          []*model.Command{strandedCmd(1, "c1", "dev-1", "nonce-abc", time.Hour)},
	}

	proc := NewCommandDeliveryProcessor(ms, nil, &recordingWriter{}, core.NewNoOpLifecycleCallbacks(),
		api, nil, &scriptedReader{states: map[string]presence.State{
			"dev-1": asserted(false, lwm2mSource),
		}})

	proc.reconcileStranded(context.Background())

	if len(api.parkCalls) != 1 || api.parkCalls[0].nonce != "nonce-abc" {
		t.Fatalf("the API and presence reader passed to the constructor must reach the stranded "+
			"pass; parks=%+v", api.parkCalls)
	}
	if proc.StrandedObserved == nil || proc.StrandedRecovered == nil || proc.StrandedSkipped == nil {
		t.Fatalf("the constructor left a stranded counter unwired: observed=%v recovered=%v skipped=%v",
			proc.StrandedObserved != nil, proc.StrandedRecovered != nil, proc.StrandedSkipped != nil)
	}
}
