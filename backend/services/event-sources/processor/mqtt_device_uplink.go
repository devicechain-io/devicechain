// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// A DEVICE'S OWN PAYLOAD, OVER ITS OWN MQTT CONNECTION.
//
//	{instance}/{tenant}/devices/{device}/uplink
//
// The same capability as POST .../devices/{token}/uplink, on the device's broker connection: the
// device publishes the bytes of its own message, and the decoder on its device type's profile turns
// them into events. Everything past the carrier is the code the HTTP route runs (carryDeviceBytes,
// the plan cache, DecoderCharge, the integration-uplinks stream, the decode in device-management
// with its budgets, health and samples): this file only carries a captured message there.
//
// 🔴 THE IDENTITY IS THE BROKER'S, NOT A CREDENTIAL'S. The device authenticated at CONNECT and its
// grant confines it to its OWN uplink topic (the token is spliced into the signed grant), so the
// device in the subject is authorized, and a device can publish a payload to be decoded as no one
// else. A captured message carries the broker's assertion exactly as an events message does (the decode
// worker hands the mark in), and the HTTP route's credential headers have no counterpart here.
//
// 🔴 THE TOPIC IS THE OPT-IN. A payload on the events topic is never decoded by a profile decoder
// (the JSON decoder refuses the uplink event type by name, and a profile decoder never sees an
// events message); a device uses the decoder by publishing here, and the grant to publish here is
// the operator's (infrastructure.nats.mqttDeviceUplinks).
//
// MQTT has no response to carry a refusal back to the device, so every outcome the HTTP route
// answers with a status is accounted here instead: a payload that cannot be carried (no decoder,
// unknown device, over a cap) is routed to failed-decode with its cause, and a tenant over its
// ceiling is shed at the same two gates as an events message. A failure to ASK (device-management
// down, an old one without the query) or to publish is left unacked, so the broker redelivers it,
// paced by AckWait, and only a message that fails every delivery is dead-lettered.

// Failure causes of a device uplink on MQTT, recorded on failed-decode.
var (
	// ErrDeviceUplinksNotServed is a captured raw payload on a source with no device-uplink route
	// behind it (device-management cannot be reached from this service).
	ErrDeviceUplinksNotServed = errors.New("device uplinks are not served by this event source")
	// ErrUplinkNoDecoder is a raw payload from a device whose type has no payload decoder. Never
	// flattened: the bytes are the device's own, not the DeviceChain envelope.
	ErrUplinkNoDecoder = errors.New("no payload decoder is attached to this device's type")
	// ErrUplinkNotAsserted is a raw payload whose device the platform's broker did not assert: a
	// wiring defect, not a device's doing.
	ErrUplinkNotAsserted = errors.New("a device uplink arrived without the broker's assertion")
	// ErrUplinkDeviceUnknown is a raw payload from a device no one registered.
	ErrUplinkDeviceUnknown = errors.New("no device is registered under the token the broker confined this publish to")
)

// uplinkPublishSlots bounds the uplink publishes and plan lookups in flight at once. A decode worker
// hands a message to a slot and moves on, so a tenant's raw payloads cannot hold every worker for a
// PubAck or a lookup each while telemetry waits. A worker never waits for a slot either: with all of
// them taken the payload is left unacked and offered again by the broker.
const uplinkPublishSlots = 64

// brokerAssertedCredential is the DeviceCredential of a device the broker authenticated.
var brokerAssertedCredential = integration.DeviceCredential{Type: "BROKER_ASSERTED", AssertedByBroker: true}

// uplinkRunner carries captured raw payloads to the integration-uplinks stream. One per start.
type uplinkRunner struct {
	rt       *DeviceUplinkRoute
	readings ReadingGate
	failed   func(string, string, []byte, error) error
	slots    chan struct{}
	// refreshing bounds the background refreshes of plans served stale.
	refreshing chan struct{}
	inflight   sync.WaitGroup
}

func newUplinkRunner(rt *DeviceUplinkRoute, readings ReadingGate, failed func(string, string, []byte, error) error) *uplinkRunner {
	return &uplinkRunner{rt: rt, readings: readings, failed: failed, slots: make(chan struct{}, uplinkPublishSlots),
		refreshing: make(chan struct{}, planRefreshSlots)}
}

// wait blocks until every publish handed off has reported its outcome.
func (u *uplinkRunner) wait() {
	if u != nil {
		u.inflight.Wait()
	}
}

func (u *uplinkRunner) count(result string) {
	if u != nil && u.rt != nil && u.rt.Message != nil {
		u.rt.Message(result)
	}
}

// terminal records a payload that can never be carried. Writing the record is a network publish, so
// off a slot it is handed to one (a decode worker never waits on it, whatever the failed-decode
// stream is doing); with every slot taken the payload is left unacked and offered again.
func (u *uplinkRunner) terminal(source string, raw rawMessage, result string, cause error, inSlot bool) {
	if inSlot {
		u.recordNow(source, raw, result, cause)
		return
	}
	if !u.take() {
		u.busy(raw)
		return
	}
	u.inflight.Add(1)
	go func() {
		defer u.inflight.Done()
		defer u.release()
		u.recordNow(source, raw, result, cause)
	}()
}

// recordNow records a payload that can never be carried and settles the message on whether the
// record reached failed-decode, as the events path does for an undecodable payload. It waits on the
// failed-decode publish, so only a slot, or a wiring defect that is not worth a slot, calls it.
func (u *uplinkRunner) recordNow(source string, raw rawMessage, result string, cause error) {
	u.count(result)
	if u == nil || u.failed == nil {
		raw.settle(nil)
		return
	}
	if routeErr := u.failed(source, raw.tenant, raw.payload, cause); routeErr != nil {
		raw.settle(fmt.Errorf("%w: %w", ErrDeadLetterUnavailable, routeErr))
		return
	}
	raw.settle(nil)
}

// looksLikeJSONObject reports whether bytes start (after whitespace) with '{'. MQTT carries no
// content type, so this is what stands in for the HTTP route's JSON content type: it only decides
// whether to TRY the parse, and bytes that then do not parse are carried as a frame, not refused.
//
// It is strict where the HTTP parse is lenient: json.Valid refuses bytes after the object, so
// `{"t":1}junk` is carried as a frame and never read as the reading `{"t":1}`.
func looksLikeJSONObject(b []byte) bool {
	t := bytes.TrimLeft(b, " \t\r\n")
	return len(t) > 0 && t[0] == '{' && json.Valid(b)
}

// planLookupTimeout bounds how long a raw payload waits for device-management to answer the plan
// question. It is short on purpose: the wait is in a publish slot, never on a decode worker, and
// a payload that times out is left for redelivery, not recorded against the device.
var planLookupTimeout = 3 * time.Second

// planRefreshSlots bounds the background refreshes of plans served stale.
const planRefreshSlots = 8

// run carries one captured raw payload. It NEVER waits on the network on a decode worker: the
// five workers are shared with every tenant's telemetry, so a device-management that stops
// answering must not stop them.
//
//   - A plan the cache holds (fresh, or stale and so refreshed in the background) is used at once and
//     the worker only builds the event; the publish is handed to a slot.
//   - A plan it does not hold is asked for in a slot, with a short deadline, and the rest of the
//     work continues there. With every slot busy the payload is left unacked and the broker offers it
//     again, paced by AckWait, rather than the worker waiting for a slot.
//
// The message is settled exactly once, by this call or by the slot's goroutine.
//
// mark is the broker's assertion for this message, which only the decode worker may read (see
// brokerAssertion): it stamps the event, and the worker has already refused a message without one.
func (u *uplinkRunner) run(source string, raw rawMessage, mark func(*model.UnresolvedEvent)) {
	if u == nil || u.rt == nil {
		u.recordNow(source, raw, DeviceUplinkInternalError, ErrDeviceUplinksNotServed)
		return
	}
	// Identity: the subject names the device and the broker confined the publisher to it.
	if mark == nil || raw.device == "" {
		u.recordNow(source, raw, DeviceUplinkInternalError, ErrUplinkNotAsserted)
		return
	}
	if len(raw.payload) > maxBodyBytes {
		u.terminal(source, raw, DeviceUplinkTooLarge, fmt.Errorf("the payload is %d bytes, over the limit of %d", len(raw.payload), maxBodyBytes), false)
		return
	}

	if look, fresh, ok := u.rt.Plans.Peek(raw.tenant, raw.device, brokerAssertedCredential); ok {
		if !fresh {
			u.refresh(raw)
		}
		u.carry(source, raw, mark, look, false)
		return
	}
	if !u.take() {
		u.busy(raw)
		return
	}
	u.inflight.Add(1)
	go func() {
		defer u.inflight.Done()
		defer u.release()
		ctx, cancel := context.WithTimeout(context.Background(), planLookupTimeout)
		look, err := u.rt.Plans.Lookup(ctx, raw.tenant, raw.device, brokerAssertedCredential)
		cancel()
		if err != nil {
			// Not knowing is neither "no such device" nor "no decoder": leave it for redelivery.
			log.Warn().Err(err).Str("tenant", raw.tenant).Str("device", raw.device).
				Msg("Device decoder lookup failed; leaving the uplink unacked for redelivery")
			u.count(DeviceUplinkPlanUnavail)
			raw.settle(err)
			return
		}
		u.carry(source, raw, mark, look, true)
	}()
}

// take claims a slot without waiting; release returns it.
func (u *uplinkRunner) take() bool {
	select {
	case u.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (u *uplinkRunner) release() { <-u.slots }

// busy leaves a payload unacked because every slot is taken, so the broker offers it again.
func (u *uplinkRunner) busy(raw rawMessage) {
	u.count(DeviceUplinkBackpressure)
	raw.settle(errUplinkBusy)
}

// errUplinkBusy is the outcome of a raw payload that found every publish slot taken.
var errUplinkBusy = errors.New("every device-uplink slot is busy; the payload is left for redelivery")

// refresh re-asks device-management about a plan that was served stale, in the background and
// off every worker, so the next payload finds it fresh. A full set of refreshes skips this one.
func (u *uplinkRunner) refresh(raw rawMessage) {
	select {
	case u.refreshing <- struct{}{}:
	default:
		return
	}
	u.inflight.Add(1)
	go func() {
		defer u.inflight.Done()
		defer func() { <-u.refreshing }()
		ctx, cancel := context.WithTimeout(context.Background(), planLookupTimeout)
		defer cancel()
		_, _ = u.rt.Plans.Lookup(ctx, raw.tenant, raw.device, brokerAssertedCredential)
	}()
}

// carry is everything after the plan is known: the verdicts on the device and its decoder, the
// carrier, the reading ceiling, the event and the publish. inSlot says whether the caller already
// holds a publish slot (and runs on its goroutine) or this is a decode worker, which hands the
// publish to a slot and returns.
func (u *uplinkRunner) carry(source string, raw rawMessage, mark func(*model.UnresolvedEvent), look integration.DeviceLookup, inSlot bool) {
	if !look.Authenticated {
		u.terminal(source, raw, DeviceUplinkUnauthorized, ErrUplinkDeviceUnknown, inSlot)
		return
	}
	plan := look.Plan
	if !plan.Present {
		u.terminal(source, raw, DeviceUplinkNoDecoder, ErrUplinkNoDecoder, inSlot)
		return
	}

	carried, charge, refusal, notJSON := carryDeviceBytes(raw.payload, looksLikeJSONObject(raw.payload), plan)
	if notJSON != nil {
		// Bytes that begin like an object and are not one are simply bytes.
		carried, charge, refusal, _ = carryDeviceBytes(raw.payload, false, plan)
	}
	if refusal != nil {
		res := DeviceUplinkBadRequest
		if refusal.uplink == UplinkTooLargeInput {
			res = DeviceUplinkTooLarge
		}
		u.terminal(source, raw, res, fmt.Errorf("%s: %w", refusal.uplink, refusal.err), inSlot)
		return
	}

	if !u.readings(source, raw.tenant, raw.meterAt, raw.redelivery, raw.origin, charge) {
		u.count(DeviceUplinkRateLimited)
		if raw.shed != nil {
			raw.shed()
		}
		raw.settle(nil)
		return
	}
	if u.rt.Charged != nil {
		u.rt.Charged(charge)
	}

	at := raw.receivedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	event := &model.UnresolvedEvent{
		Device:        raw.device,
		OccurredTime:  at,
		ProcessedTime: at,
		EventType:     eventtype.IntegrationUplink,
	}
	mark(event)

	publish := func() {
		err := u.rt.Publish(source, raw.tenant, event, carried, raw.dedupID)
		switch {
		case err == nil:
			u.count(DeviceUplinkAccepted)
			raw.settle(nil)
		case errors.Is(err, ErrEventNotBuilt):
			// Terminal, like every build drop (a retry would fail identically), and recorded with its
			// cause like every other payload that cannot be carried.
			u.terminal(source, raw, DeviceUplinkInternalError, err, true)
		case errors.Is(err, messaging.ErrStreamBackpressure):
			u.count(DeviceUplinkBackpressure)
			raw.settle(err)
		default:
			u.count(DeviceUplinkPublishFailed)
			raw.settle(err)
		}
	}
	if inSlot {
		publish()
		return
	}
	if !u.take() {
		u.busy(raw)
		return
	}
	u.inflight.Add(1)
	go func() {
		defer u.inflight.Done()
		defer u.release()
		publish()
	}()
}
