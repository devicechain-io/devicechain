// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/transport"
	"github.com/rs/zerolog/log"
)

// StrandedSentGrace is how long a command must have been reading SENT, with nothing
// learned about it, before this pass will treat it as stranded rather than in flight.
//
// 🔑 IT IS DERIVED, NOT CHOSEN, AND EVERY TERM IS SOMEONE ELSE'S NUMBER. The question it
// answers is "could the platform still be working on this?", and the platform's own
// retry budget is the only honest answer:
//
//   - messaging.MaxDeliver * messaging.AckWait is the longest the broker can still be
//     redelivering the dispatch — JetStream holds an unacked message for AckWait before
//     handing it to someone else, MaxDeliver times over.
//   - config.RedeliveryInterval adds the one sweep tick that can elapse before the
//     platform next looks at the row at all.
//
// Anything inside that window is a command still being worked on, and parking it would
// race the very machinery that is about to resolve it. Today the sum is 330s.
//
// 🔴 THE POINT OF DERIVING IT IS THAT THE DRIFT WOULD OTHERWISE BE SILENT. Writing 330s
// here as a literal would keep working — wrongly — after any of those three values
// changed: raising AckWait would make this pass start parking commands the broker was
// still redelivering, and nothing would fail, log, or alert. The reading would just
// quietly stop meaning what it says. messaging.AckWait was exported for this.
//
// ⚠️ It is a FLOOR on staleness, never a ceiling: the row's own TTL still governs when it
// expires, and this pass must never race ExpireStale. It does not, by construction — the
// default TTL is days and this horizon is minutes.
const StrandedSentGrace = messaging.MaxDeliver*messaging.AckWait + config.RedeliveryInterval*time.Second

// strandedPageSize bounds one stranded-reconcile pass's read of the in-flight set.
//
// The walk resumes from a cursor, so this bounds the work per pass rather than the set
// that is ever examined.
const strandedPageSize = 500

// Skip reasons. These are Prometheus label values, so they are a closed vocabulary and
// each one names a DIFFERENT question the pass could not answer — an operator reading the
// series needs to know which.
const (
	// skipTransport: the device's projected transport is not one this pass may act on.
	// On an MQTT-heavy instance this is the DOMINANT series BY DESIGN, and an alert must
	// not read it as a fault. See the gate in reconcileStrandedCommand.
	skipTransport = "transport"
	// skipPresenceUnknown: the presence read could not cover this device, so its
	// transport is unknown. Fails closed.
	skipPresenceUnknown = "presence_unknown"
	// skipNoNonce: the row is SENT but carries no dispatch nonce, so no park can name
	// the dispatch that this pass observed.
	//
	// 🔑 THIS SERIES SHOULD BE FLAT AT ZERO, WHICH IS WHY IT IS WORTH COUNTING. Both
	// writers that put a row into SENT (MarkSent and MarkSentByToken) stamp status,
	// sent_time and dispatch_nonce in ONE update, so a SENT row without a nonce should
	// not exist. A non-zero rate here means something reached SENT by another route, and
	// that is worth knowing about for reasons well beyond this pass.
	skipNoNonce = "no_nonce"
	// skipRaced: the park matched no row, so the command left SENT between the scan and
	// the write — answered, cancelled, expired, or re-dispatched. The predicate doing its
	// job, not a failure.
	skipRaced = "raced"
	// skipError: the park itself failed.
	skipError = "error"
)

// reconcileStranded parks commands that have been sitting in SENT with no outcome for
// longer than the platform could still be working on them.
//
// 🔴 WHY THIS EXISTS: A COMMAND CAN REACH SENT AND THEN BE REACHED BY NOTHING AT ALL.
// SENT has exactly three exits — a device response, a transport retiring its claim, and
// the TTL — and the first two require someone to still be holding the command. When the
// holder goes away mid-flight, the row is left with no owner, invisible to the delivery
// sweep, the wake drain and the cancel path alike, and its only remaining exit is the TTL,
// which records TIMEOUT. That verdict blames the DEVICE for a failure that was entirely
// ours, and it is exactly the mislabel the SENT/PARKED split was built to remove. This
// pass is what stops the split from having a hole in it.
//
// 🔴🔴 THE HONEST PART: A LONG-LIVED SENT ROW HAS SIX PRODUCERS, AND IN THREE OF THEM THE
// DEVICE ALREADY GOT THE COMMAND. The row is byte-identical in all six — a publish that
// failed alongside its release, a park that exhausted its retries, a transport with no
// parker wired, a device that ACTED and whose response publish then failed, a response
// that exhausted MaxDeliver, and MQTT's live-only QoS 0, where it is simply unknowable.
// Re-arming therefore ACCEPTS duplicate actuation in the cases where the device did act.
// This design confines that acceptance rather than pretending it away:
//
//   - it re-arms to PARKED, not QUEUED, so nothing is re-sent on a timer. A parked
//     command moves only when the device itself proves it is awake, and the wake drain
//     CLAIMS the row before actuating.
//   - it acts only on transports where the platform genuinely cannot have delivered
//     silently — see the gate below.
//
// The alternative is not "no duplicate actuation", it is a guaranteed TIMEOUT on a
// command the platform never delivered, written against an innocent device.
//
// Its cadence is minutes, like the hold reconciler's and for the same reason: this asks
// "has something been abandoned?", where the answer is almost always no and asking
// cheaply matters more than asking quickly.
func (cproc *CommandDeliveryProcessor) reconcileStranded(ctx context.Context) {
	ran, err := cproc.Api.TryStrandedLock(ctx, func() error {
		cproc.reconcileStrandedPage(ctx)
		return nil
	})
	if err != nil {
		log.Error().Err(err).Msg("could not acquire the stranded-reconcile lock")
		return
	}
	if !ran {
		log.Debug().Msg("Another replica holds the stranded-reconcile lock; skipping this pass.")
	}
}

// reconcileStrandedPage walks one bounded page of long-lived SENT commands.
func (cproc *CommandDeliveryProcessor) reconcileStrandedPage(ctx context.Context) {
	// 🔴 NO PRESENCE MEANS NO TRANSPORT, AND NO TRANSPORT MEANS DO NOTHING. The gate
	// below is an ALLOW list keyed on the device's projected source, and the projection
	// is the only place that value exists — command-delivery persists no transport of its
	// own. With no reader there is nothing to allow on, and defaulting to "act" would
	// park MQTT commands, which asserts the command went NOWHERE when a QoS-0 publish
	// cannot know that.
	//
	// 🔑 THIS IS THE SAME FAIL-CLOSED DIRECTION AS THE HOLD RECONCILER AND THE OPPOSITE
	// OF THE SWEEP'S, and all three follow one rule: never act on information you do not
	// have. The sweep's new behaviour is WITHHOLDING, so missing information means don't
	// withhold; this pass's new behaviour is PARKING, so missing information means don't
	// park. Both degrade to the behaviour that existed before the mechanism did.
	if cproc.Presence == nil {
		return
	}
	stranded, next, err := cproc.Api.StrandedSentCommands(core.WithSystemContext(ctx),
		cproc.strandedCursor, time.Now().Add(-StrandedSentGrace), strandedPageSize)
	if err != nil {
		log.Error().Err(err).Msg("unable to read long-lived SENT commands for reconciliation")
		return
	}
	cproc.strandedCursor = next
	if len(stranded) == 0 {
		return
	}
	incr(cproc.StrandedObserved, float64(len(stranded)))
	for _, batch := range groupByTenant(stranded, cproc.tenantDeleted) {
		cproc.reconcileStrandedTenantBatch(ctx, batch)
	}
}

// reconcileStrandedTenantBatch parks one tenant's stranded commands where it may.
func (cproc *CommandDeliveryProcessor) reconcileStrandedTenantBatch(ctx context.Context, batch tenantBatch) {
	tenantCtx := core.WithTenant(ctx, batch.tenant)
	states, err := cproc.Presence.StatesFor(tenantCtx, distinctDevices(batch.commands))
	if err != nil {
		// Counted on the same meter as the sweep's and the hold reconciler's read
		// failures: it is the same outage, and an operator looking at it wants one
		// number for "the gate cannot see presence".
		incr(cproc.PresenceReadErrors, 1)
		log.Warn().Err(err).Str("tenant", batch.tenant).
			Int("asked", len(distinctDevices(batch.commands))).Int("answered", len(states)).
			Msg("Could not read presence for some stranded commands' devices; those are left alone.")
	}
	for _, cmd := range batch.commands {
		cproc.reconcileStrandedCommand(tenantCtx, cmd, states, err)
	}
}

// reconcileStrandedCommand decides one long-lived SENT row's fate.
func (cproc *CommandDeliveryProcessor) reconcileStrandedCommand(ctx context.Context,
	cmd *model.Command, states map[string]presence.State, readErr error) {
	// A device the read could not cover is absent from states, and absence on a FAILED
	// read is not an answer. Acting on it would park on an unknown transport.
	if !presence.Resolved(states, cmd.DeviceToken, readErr) {
		incrLabel(cproc.StrandedSkipped, skipPresenceUnknown)
		return
	}

	// 🔴🔴 AN ALLOW LIST, NOT A DENY LIST, AND THE POLARITY IS THE WHOLE SAFETY ARGUMENT.
	// Every other transport gate on this path is a deny list, where an unclassified source
	// falls through to the permissive answer. Here that default would be catastrophic in a
	// way it is not there: this pass re-arms a command for a SECOND actuation, so falling
	// through means acting on a device the gate was written to exclude.
	//
	// The list holds LwM2M alone, and the two exclusions are decisions rather than gaps:
	//
	//   - MQTT is excluded because PARKED would be a LIE there. It asserts the command
	//     reached nothing, and MQTT dispatch is live-only QoS 0 — a publish with no
	//     subscriber is indistinguishable from one that was delivered and answered late.
	//     Nothing reads PARKED on MQTT either, so the row would simply be stuck in a
	//     state with no exit rather than in one with a wrong label. For MQTT this pass is
	//     a deliberate no-op, and the floor is exactly what it was before.
	//   - Sparkplug never reaches SENT at all: the presence gate answers Undeliverable
	//     for it, because it has no command egress by construction.
	//
	// 🔑 THE LIST IS SAFE TO KEY ON THE PROJECTED SOURCE ONLY BECAUSE THE VOCABULARY IS
	// NOW VALIDATED. The source is `transport` or `transport:qualifier`, and its MQTT/HTTP
	// form is an OPERATOR-CHOSEN event-source id — so before that id was validated, an
	// operator naming an MQTT source "lwm2m" would have projected a source this gate
	// classifies as LwM2M and acted on the very devices it excludes. Against the existing
	// deny lists the same collision is a self-inflicted false DENY; against this allow
	// list it is a false ALLOW. EventSource.Id now rejects any id that classifies as a
	// minted transport, which is what makes this gate trustworthy rather than merely
	// plausible.
	//
	// ⚠️ The source is LAST-WRITER-WINS, so for a dual-stack device this reads "the
	// transport of the last event that touched it", not "the transport it is on". That is
	// tolerable HERE and would not be everywhere: the cost of reading it wrong is a park
	// on a device that also speaks MQTT, and a parked row still only moves when the device
	// proves it is awake and the drain claims it.
	if transport.Of(states[cmd.DeviceToken].Source) != transport.LwM2M {
		incrLabel(cproc.StrandedSkipped, skipTransport)
		return
	}

	// 🔴🔴 THE PARK MUST QUOTE THE NONCE THE SCAN OBSERVED. Without it this pass re-opens
	// precisely the hole the nonce was introduced to close, and it is reachable INSIDE a
	// single pass: the scan sees (row, nonce N) → a late park lands, which retires the
	// claim WITHOUT clearing the nonce → the device wakes, the drain claims the row with a
	// FRESH nonce N' and ACTUATES it → a park predicated only on `status = 'SENT'` matches
	// that freshly-actuated row and re-arms it for a second physical actuation. Quoting N
	// makes the stale request name a dispatch that no longer exists, so it matches zero
	// rows. TestParkRefusesARowReDispatchedSinceTheScan pins this, and fails if the
	// predicate is dropped.
	if !cmd.DispatchNonce.Valid || cmd.DispatchNonce.String == "" {
		incrLabel(cproc.StrandedSkipped, skipNoNonce)
		return
	}
	parked, err := cproc.Api.ParkClaim(ctx, cmd.Token, cmd.DispatchNonce.String)
	if err != nil {
		incrLabel(cproc.StrandedSkipped, skipError)
		log.Error().Err(err).Str("command", cmd.Token).Str("device", cmd.DeviceToken).
			Msg("unable to park a command stranded in SENT")
		return
	}
	if !parked {
		incrLabel(cproc.StrandedSkipped, skipRaced)
		return
	}
	incr(cproc.StrandedRecovered, 1)
	log.Info().Str("command", cmd.Token).Str("device", cmd.DeviceToken).
		Msg("Parked a command stranded in SENT; it will be delivered when its device next wakes.")
}
