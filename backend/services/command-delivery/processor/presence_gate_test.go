// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// scriptedReader answers with whatever a test puts in it, and records what it was asked.
type scriptedReader struct {
	states map[string]presence.State
	err    error

	calls    int
	askedFor [][]string
	tenants  []string
}

func (r *scriptedReader) StatesFor(ctx context.Context, devices []string) (map[string]presence.State, error) {
	r.calls++
	r.askedFor = append(r.askedFor, devices)
	if tenant, ok := core.TenantFromContext(ctx); ok {
		r.tenants = append(r.tenants, tenant)
	}
	if r.err != nil {
		// A reader that fails outright answers for nothing — the shape the real chunked
		// reader produces when every chunk fails, which is what a whole-service outage
		// looks like from here.
		return nil, r.err
	}
	return r.states, nil
}

// absentReader reports the named devices as authoritatively absent over MQTT and knows
// nothing about any other. It is the smallest reader that produces a Hold.
type absentReader []string

func (a absentReader) StatesFor(context.Context, []string) (map[string]presence.State, error) {
	states := map[string]presence.State{}
	for _, device := range a {
		states[device] = presence.State{Known: true, Asserted: true, Active: false, Source: mqttSource}
	}
	return states, nil
}

// asserted builds a projected state for a device a transport actively reports on.
func asserted(active bool, source string) presence.State {
	return presence.State{Known: true, Asserted: true, Active: active, Source: source}
}

// sparkplugSource is the source string a Sparkplug device is ACTUALLY projected with:
// sparkplug-ingest stamps "sparkplug:"+hostId, and device-state denormalizes it verbatim.
//
// 🔴🔴 NEVER HAND-WRITE A BARE "sparkplug" IN A GATE FIXTURE. These end-to-end tests are the
// ones that could have caught the whole Undeliverable verdict being unreachable in
// production, and they did not — because they posed a source value nothing in the platform
// emits, so they exercised the deny list against an input it would never be given. The
// presence package holds the same constant with the same warning; they are separate because
// the two packages share no non-test code, which is exactly the drift this value guards.
const sparkplugSource = "sparkplug:plant-a"

// mqttSource is what a plain-MQTT device is ACTUALLY projected with, and it is NOT "mqtt":
// event-sources stamps the configured event source's own id (default "mqtt1"), while
// processor.TYPE_MQTT = "mqtt" is a config type discriminator that never reaches an event.
// Behaviour-neutral here — any source off the deny list dispatches — which is precisely why
// the fiction would never fail a test, and why it is spelled correctly instead.
const mqttSource = "mqtt1"

// TestAnAbsentDeviceHasItsCommandHeldRatherThanPublished is the gate's whole purpose.
//
// 🔴 THE BEHAVIOUR THIS REPLACES IS A SILENT LOSS, NOT A VISIBLE FAILURE. An MQTT publish
// reaches only a device connected and subscribed at that instant — the broker does not
// hold it — so a command to an absent device was published into nothing, marked SENT, and
// expired a week later as TIMEOUT: a permanent record blaming a device that was never
// given the command.
func TestAnAbsentDeviceHasItsCommandHeldRatherThanPublished(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(false, mqttSource)}}

	proc.sweepLocked(context.Background())

	if writer.count() != 0 {
		t.Fatalf("a command to an authoritatively absent device must not be published; it would be "+
			"dropped by the broker and then blamed on the device as a TIMEOUT (published %d)", writer.count())
	}
	if len(api.held) != 1 || api.held[0] != 1 {
		t.Fatalf("the command must be moved to HELD, got held=%v", api.held)
	}
	if len(api.markedSent) != 0 {
		t.Fatalf("a held command must not be claimed for dispatch, marked sent = %v", api.markedSent)
	}
}

// TestAPresentDeviceStillDispatches is the counterweight, and it is not optional: every
// assertion above is also satisfied by a gate that holds EVERYTHING, which would stop
// command delivery platform-wide while looking exactly like a working gate.
func TestAPresentDeviceStillDispatches(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, mqttSource)}}

	proc.sweepLocked(context.Background())

	if writer.count() != 1 {
		t.Fatalf("a present device's command must still be published, got %d", writer.count())
	}
	if len(api.held) != 0 {
		t.Fatalf("nothing should have been held, got %v", api.held)
	}
}

// TestTheGateFailsOpen covers the three separate ways the gate can have no answer. All
// three must DISPATCH.
//
// 🔴 THE DIRECTION IS THE POINT. The gate exists to stop commands being thrown at devices
// that cannot receive them; it is not an authority on whether the platform may dispatch at
// all. A gate that withheld when it could not reach device-state would convert one
// service's outage — or an instance that simply does not deploy device-state, a supported
// profile — into a platform-wide command stall. That is strictly worse than the silent
// loss the gate prevents, and unlike it, it stops commands that WOULD have arrived.
func TestTheGateFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		wire  func(*CommandDeliveryProcessor)
		why   string
		reads int
	}{
		{
			"no gate wired at all",
			func(p *CommandDeliveryProcessor) { p.Presence = nil },
			"an instance whose profile has no device-state must deliver exactly as it did before " +
				"the gate existed",
			0,
		},
		{
			"the projection read failed",
			func(p *CommandDeliveryProcessor) {
				p.Presence = &scriptedReader{err: errors.New("device-state unreachable")}
			},
			"a device-state outage must not stall command delivery",
			1,
		},
		{
			"the device has no row in the projection",
			func(p *CommandDeliveryProcessor) {
				p.Presence = &scriptedReader{states: map[string]presence.State{"someone-else": asserted(false, mqttSource)}}
			},
			"deviceStatesByDeviceToken returns only rows that EXIST — a device that has never " +
				"produced an event is simply absent from the response, and reading that silence " +
				"as 'offline' would hold every command on a fresh instance",
			1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
			writer := &recordingWriter{}
			proc := procWith(api, writer)
			c.wire(proc)

			proc.sweepLocked(context.Background())

			if writer.count() != 1 {
				t.Fatalf("published %d, want 1\n%s", writer.count(), c.why)
			}
			if len(api.held) != 0 {
				t.Fatalf("nothing may be held here: %s (held=%v)", c.why, api.held)
			}
		})
	}
}

// TestAFailedPresenceReadIsCOUNTED.
//
// 🔴 THE COUNTER IS THE ONLY EVIDENCE THAT THE FAIL-OPEN HAPPENED. Every other symptom of
// a broken projection read is an absence: commands flow, nothing is held, no error
// surfaces to any caller — which is indistinguishable from a fleet that is entirely
// present and a gate working perfectly. Without this meter the gate can be dead for
// months while the silent losses it exists to prevent quietly resume.
//
// It is asserted rather than merely described because a comment claiming a hazard is
// covered, with nothing covering it, is the exact shape that has produced three defects
// in this area already.
func TestAFailedPresenceReadIsCOUNTED(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	proc := procWith(api, &recordingWriter{})
	proc.Presence = &scriptedReader{err: errors.New("device-state unreachable")}
	proc.PresenceReadErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "read_errors"})

	proc.sweepLocked(context.Background())

	if got := testutil.ToFloat64(proc.PresenceReadErrors); got != 1 {
		t.Fatalf("a failed presence read must be counted, got %v — the fail-open is otherwise "+
			"indistinguishable from a fleet that is entirely present", got)
	}
}

// TestAHoldIsCOUNTED, for the same reason in the other direction: holds placed with
// nothing ever released is the signature of a gate that has become a one-way door, and
// neither number means anything without the other.
func TestAHoldIsCOUNTED(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	proc := procWith(api, &recordingWriter{})
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(false, mqttSource)}}
	proc.HoldsPlaced = prometheus.NewCounter(prometheus.CounterOpts{Name: "holds_placed"})

	proc.sweepLocked(context.Background())

	if got := testutil.ToFloat64(proc.HoldsPlaced); got != 1 {
		t.Fatalf("a placed hold must be counted, got %v", got)
	}
}

// TestALostHoldIsNotCOUNTED. The counter measures holds this pass PLACED, not holds it
// attempted — a zero-row write lost to another dispatcher placed nothing. Counting the
// attempt would make the meter drift upward against HoldsReleased under exactly the
// contention it exists to make visible.
func TestALostHoldIsNotCOUNTED(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	api.holdLoses = true
	proc := procWith(api, &recordingWriter{})
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(false, mqttSource)}}
	proc.HoldsPlaced = prometheus.NewCounter(prometheus.CounterOpts{Name: "holds_placed_lost"})

	proc.sweepLocked(context.Background())

	if got := testutil.ToFloat64(proc.HoldsPlaced); got != 0 {
		t.Fatalf("a hold that lost its race placed nothing and must not be counted, got %v", got)
	}
}

// TestAnUndeliverableTransportFailsTheCommandRatherThanHoldingIt.
//
// 🔑 HOLDING WOULD BE A DIFFERENT LIE, NOT A SAFER ONE. HELD means "waiting for the device
// to come back". Sparkplug devices do come back — on every rebirth — and delivery still
// cannot happen, because nothing bridges our command stream to a Sparkplug host and the
// devices sit on the customer's own MQTT infrastructure. The row would sit until its TTL
// dragged it to EXPIRED, occupying the tenant's undelivered ceiling the whole time and
// letting one such fleet crowd out commands to devices that CAN receive them.
func TestAnUndeliverableTransportFailsTheCommandRatherThanHoldingIt(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(true, sparkplugSource)}}

	proc.sweepLocked(context.Background())

	if writer.count() != 0 {
		t.Fatalf("a command to a transport with no command path must not be published into the void, "+
			"got %d", writer.count())
	}
	if len(api.held) != 0 {
		t.Fatalf("it must be FAILED, not held — a hold waits for a return that changes nothing "+
			"while burning the tenant's ceiling for a full TTL (held=%v)", api.held)
	}
	if len(api.undeliverable) != 1 || api.undeliverable[0] != 1 {
		t.Fatalf("the command must be failed as undeliverable, got %v", api.undeliverable)
	}
	// The reason reaches the tenant on the command row, so it must name the platform
	// rather than the device — an operator reading "the device did not respond" goes and
	// checks hardware that is working perfectly.
	if reason := api.failReasons[0]; !strings.Contains(reason, "transport") {
		t.Fatalf("the recorded reason must explain WHY, got %q", reason)
	}
}

// TestOnePresenceReadPerTenant pins the batching.
//
// 🔑 A READ PER COMMAND WOULD PUT A NETWORK CALL BETWEEN THE PLATFORM AND EVERY DISPATCH.
// The natural shape — look up presence inside the per-command loop — is correct and
// unusable: a tenant with a thousand queued commands would issue a thousand GraphQL round
// trips per sweep tick, inside the lock every other replica is waiting on. The read is
// per-tenant, and within a tenant the devices are deduplicated.
func TestOnePresenceReadPerTenant(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queuedFor(1, "c1", "acme"),
		queuedFor(2, "c2", "acme"),
		queuedFor(3, "c3", "acme"),
		queuedFor(4, "c4", "other"),
	}}
	// Two of acme's three commands target the SAME device, so its batch must ask about
	// two devices rather than three.
	api.pending[1].DeviceToken = api.pending[0].DeviceToken
	reader := &scriptedReader{}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = reader

	proc.sweepLocked(context.Background())

	if reader.calls != 2 {
		t.Fatalf("expected one presence read per tenant (2), got %d", reader.calls)
	}
	if got := reader.askedFor[0]; len(got) != 2 {
		t.Fatalf("a tenant's batch must be deduplicated by device, asked for %v", got)
	}
	// 🔴 Each read must carry ITS OWN tenant. The sweep loads commands cross-tenant under
	// a system context; a read that inherited that context would either fail closed or —
	// worse, if the query is not tenant-scoped — answer with another tenant's devices.
	if len(reader.tenants) != 2 || reader.tenants[0] != "acme" || reader.tenants[1] != "other" {
		t.Fatalf("each presence read must be scoped to its own tenant, got %v", reader.tenants)
	}
}

// TestADeletedTenantIsNeverAskedAboutPresence.
//
// The ADR-077 refusal happens before any batch is built, so a deleted tenant produces no
// presence read at all. Asking would be harmless in itself; the assertion is here because
// it is the cheap, visible proof that the refusal really does precede the gate rather than
// sitting somewhere inside it, where a later edit could reorder them without any test
// noticing.
func TestADeletedTenantIsNeverAskedAboutPresence(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queuedFor(1, "c1", "acme"),
		queuedFor(2, "c2", "other"),
	}}
	reader := &scriptedReader{}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = reader
	proc.TenantDeleted = func(tenant string) bool { return tenant == "acme" }

	proc.sweepLocked(context.Background())

	if reader.calls != 1 || len(reader.tenants) != 1 || reader.tenants[0] != "other" {
		t.Fatalf("only the live tenant may be asked about presence, got %v", reader.tenants)
	}
}

// TestAHoldThatLosesItsRaceIsBenign.
//
// 🔴 THIS IS WHY THE HOLD WRITE IS CONDITIONAL. The sweep SELECTs its batch and then walks
// it, so a row it observed as QUEUED can be claimed SENT by another dispatcher — the LwM2M
// wake drain — while the walk is still in progress. HoldCommand pins `status = 'QUEUED'`,
// so the write loses that race and reports zero rows; without the predicate it would stamp
// HELD over a command that was physically dispatched microseconds earlier, making it
// claimable again so the next tick actuates the device a SECOND time.
//
// The sweep's part of that contract is small but load-bearing: a lost hold must not be
// treated as an error, must not be counted, and must not be retried into anything else.
func TestAHoldThatLosesItsRaceIsBenign(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{queued(1, "c1")}}
	api.holdLoses = true
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{"dev-c1": asserted(false, mqttSource)}}

	proc.sweepLocked(context.Background())

	if len(api.held) != 1 {
		t.Fatalf("the hold must still be attempted, got %v", api.held)
	}
	if writer.count() != 0 {
		t.Fatalf("losing the hold race must not fall through to publishing — the dispatcher that "+
			"won the row is already sending it (published %d)", writer.count())
	}
}

// TestAMixedTenantBatchTakesThreeDifferentActions is the gate working as one pass rather
// than three independent behaviours. Each verdict must apply to its own command without
// disturbing the others: one absent device's hold must not stall the present device beside
// it, and an undeliverable transport must not fail the whole batch.
func TestAMixedTenantBatchTakesThreeDifferentActions(t *testing.T) {
	api := &fakeApi{lockAvailable: true, pending: []*model.Command{
		queued(1, "present"),
		queued(2, "absent"),
		queued(3, "sparkplug"),
	}}
	writer := &recordingWriter{}
	proc := procWith(api, writer)
	proc.Presence = &scriptedReader{states: map[string]presence.State{
		"dev-present":   asserted(true, mqttSource),
		"dev-absent":    asserted(false, mqttSource),
		"dev-sparkplug": asserted(true, sparkplugSource),
	}}

	proc.sweepLocked(context.Background())

	if got := writer.devices; len(got) != 1 || got[0] != "dev-present" {
		t.Fatalf("exactly the present device's command may be published, got %v", got)
	}
	if len(api.held) != 1 || api.held[0] != 2 {
		t.Fatalf("exactly the absent device's command may be held, got %v", api.held)
	}
	if len(api.undeliverable) != 1 || api.undeliverable[0] != 3 {
		t.Fatalf("exactly the undeliverable device's command may be failed, got %v", api.undeliverable)
	}
}
