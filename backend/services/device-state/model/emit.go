// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// demotionDedupPrefix namespaces every dedup id this service publishes, exactly as
// the ingest emitters namespace theirs ("lw", "sp", "bp"). The InboundEvents dedup
// window is STREAM-scoped, so two producers minting the same id would silently
// suppress each other's messages; the prefix is what makes that structurally
// impossible rather than merely unlikely.
const demotionDedupPrefix = "ds"

// demotionDedupState is the state token a demotion contributes to its dedup id. The
// ingest emitter's presence ids use "0" for DISCONNECTED and "1" for CONNECTED; this
// is the third member of that vocabulary and must never be re-used for either of
// them.
const demotionDedupState = "2"

// ErrDemotionPublish is the sentinel every failure of the EMIT half wraps, so the
// resolver can tell a caller's bad argument (returned verbatim — it names only what
// the caller sent) from a broker failure (sanitized — a publish error carries subject,
// stream and cluster addressing, and this is a tenant-facing plane) without matching
// on the text of somebody else's error.
var ErrDemotionPublish = errors.New("the presence demotion could not be published")

// ErrNoDemotionEmitter is returned when a demotion is requested on an Api that was
// never given a writer. It fails closed rather than reporting a successful demotion
// of zero devices: this service's messaging components are wired during startup, so a
// nil emitter means the process is not able to emit, not that there was nothing to do.
var ErrNoDemotionEmitter = fmt.Errorf("%w: device-state has no inbound-events writer wired", ErrDemotionPublish)

// DemotionEmitter writes one presence DEMOTION as a StateChange UnresolvedEvent onto
// the shared inbound-events stream (ADR-067 demotion). It is deliberately the SAME
// wire contract, the same stream and the same resolver as a transport's own presence
// advisory: an operator demotion is not a private back door into the projection, it
// is a claim that travels the pipeline and is judged by the same ordering guard.
//
// 🔴 THAT IS THE WHOLE DESIGN, AND WRITING THE ROW DIRECTLY WOULD HAVE BEEN EASIER.
// device-state owns device_states and could have UPDATEd them in place. It must not:
// the DETECT engine (event-processing) keeps its own presence cursor keyed on the
// identical predicate, and a projection that moved without an event would leave the
// two consumers describing different devices — the precise divergence core/presence
// exists to prevent. Going through the stream also inherits the ordering guard, the
// idempotency window, and the event history for free.
type DemotionEmitter struct {
	writer messaging.MessageWriter
	now    func() time.Time
}

// NewDemotionEmitter binds an emitter to a durable inbound-events writer and a clock
// (nil ⇒ time.Now).
func NewDemotionEmitter(writer messaging.MessageWriter, now func() time.Time) *DemotionEmitter {
	if now == nil {
		now = time.Now
	}
	return &DemotionEmitter{writer: writer, now: now}
}

// SetDemotionEmitter wires the emitter onto the Api. It is called during startup,
// from the NATS-components callback, because that is the one place where both the
// Api and an initialized NatsManager are in hand.
func (api *Api) SetDemotionEmitter(e *DemotionEmitter) { api.demotions = e }

// demotionReason composes the Reason stamped on every emitted demotion. Reason is
// descriptive metadata the pipeline never reads for ordering or authorization, and
// this is the only thing that records WHO released a source's custody of a device —
// so the actor is prefixed here, next to the emitter, rather than left to each caller
// to format the same way.
//
// The actor is taken from the authenticated subject, never from a caller-supplied
// argument, so the provenance cannot be forged. The operator's own words follow the
// colon.
func demotionReason(actor, reason string) string {
	return fmt.Sprintf("operator-demotion(actor=%s): %s", actor, reason)
}

// demotionDedupID keys a demotion on (tenant, device, session), which is exactly the
// thing being released — so a retry, or two replicas answering the same operator
// click, collapse onto one event.
//
// 🔑 NO NONCE, DELIBERATELY, AND THE OPPOSITE CHOICE IS RIGHT FOR THE OTHER PRODUCER.
// This mutation is a RE-DERIVATION: "release session S of device D", a statement whose
// truth does not depend on when it was said, so two of them are the same event and
// must collapse. A source's own boundary drain is an OBSERVATION per pass ("as of this
// tick, this row's authority is stale") and carries a nonce, because a tick whose write
// failed must not be suppressed by the previous tick's dedup entry.
//
// It is a fixed-width fnv-64a over NUL-separated parts behind the origin prefix, the
// same construction the ingest emitters use: the dedup window holds every id it has
// seen in memory for its full duration, so id size is a memory cost.
func demotionDedupID(tenant, deviceToken string, sessionId uint64) string {
	h := fnv.New64a()
	for _, p := range []string{"sc", tenant, deviceToken, strconv.FormatUint(sessionId, 10), demotionDedupState} {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return demotionDedupPrefix + strconv.FormatUint(h.Sum64(), 36)
}

// EmitDemotion publishes one device's demotion. source is the EVENT SOURCE whose
// custody is being released; sessionId is the session the projection row currently
// holds; occurredAt is the demotion's own instant.
//
// 🔴 source IS THE ARGUMENT AND NEVER "device-state". MergeDeviceState refreshes
// device_states.source from every resolved event that carries one, so emitting under
// this service's own name would rewrite the provenance of every row it demoted —
// leaving a fleet filed under a source that has no transport, no command path, and no
// reconciler, which is a worse state than the frozen one the demotion came to fix.
//
// The tenant is read from context rather than passed, so the subject the message is
// published to and the tenant its dedup id is scoped by cannot disagree. A call with
// no tenant in context is refused, not published unscoped.
func (e *DemotionEmitter) EmitDemotion(ctx context.Context, source, deviceToken string,
	sessionId uint64, occurredAt time.Time, reason string) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return core.ErrNoTenant
	}
	occurred := occurredAt.UTC()
	occStr := occurred.Format(time.RFC3339Nano)
	uev := &esmodel.UnresolvedEvent{
		Source:        source,
		Device:        deviceToken,
		EventType:     esmodel.StateChange,
		OccurredTime:  occurred,
		ProcessedTime: e.now().UTC(),
		// The event is minted by a platform service, not received from a device, so it
		// carries no per-event credential and resolves on its self-asserted token —
		// the same standing the transport-authenticating ingest services have. The
		// marker is not device-forgeable: nothing on the device->inbound-events gateway
		// can set it, and the resolver confines the bypass to Measurement and
		// StateChange, which is what stops this trust from widening later.
		AuthenticatedTransport: true,
		Payload: &esmodel.UnresolvedStateChangePayload{
			State:  esmodel.PresenceDemoted,
			Reason: reason,
			// EMPTY, and the resolver REFUSES a demotion that sets it. A compare-and-set
			// precondition on a demotion is incoherent by construction: acceptsDemotion
			// already matches SessionId against the stored session, so an expectation
			// either restates that or contradicts it.
			ExpectedSessionId: "",
			// The session being released, read off the row. This is the whole mechanism:
			// a demotion applies only against the session the projection still holds, so
			// a row that has moved on — a real reconnect, a repair — refuses it.
			SessionId:    strconv.FormatUint(sessionId, 10),
			OccurredTime: &occStr,
		},
	}
	encoded, err := esproto.MarshalUnresolvedEvent(uev)
	if err != nil {
		return err
	}
	return e.writer.WriteMessages(ctx, messaging.Message{
		Key:     []byte(deviceToken),
		Value:   encoded,
		DedupID: demotionDedupID(tenant, deviceToken, sessionId),
	})
}

// PresenceDemotionResult is what one page of a demotion walk did. It is FLAT — four
// scalars, no refusal union — because every way this call can fail short of an error
// is a per-row condition that a count answers better than a type would.
type PresenceDemotionResult struct {
	// Scanned is how many ASSERTED rows this page examined. It is the caller's
	// TERMINATION SIGNAL: keep walking while Scanned == limit, stop when it is less.
	// Demoted cannot serve that purpose — a page of entirely skippable rows demotes
	// nothing and is not the end of the set.
	Scanned int32
	// Demoted is how many demotion events were published.
	Demoted int32
	// Skipped is Scanned − Demoted: rows this page could not demote. See
	// DemoteAssertedPresence for the three conditions, each of which is logged with
	// the device it concerns.
	Skipped int32
	// LastId is the id of the last row SCANNED (not the last demoted), which is the
	// cursor for the next page. Zero when the page was empty. Resuming from the last
	// row KEPT would re-read every skipped row on every subsequent page, which for a
	// permanently-skippable row is a walk that never ends.
	LastId uint64
}

// DemoteAssertedPresence walks ONE PAGE of a source's ASSERTED device states and
// publishes a demotion for each, returning the projection's rows to INFERRED (ADR-067
// demotion). It is the manual door beside the automatic release a source performs at
// its own disable boundary — the repair for a fleet whose presence tap has stopped
// running, where every asserted row is frozen at whatever it last held.
//
// tokens is THREE-STATE and the three states are not interchangeable — see
// AssertedStatesForDemotion, which owns that rule.
//
// A ROW IS SKIPPED, NOT DEMOTED, IN TWO CASES, both conditions under which the
// emitted event could not have applied anyway. Skipping them is what turns a silent
// permanent no-op into a number the operator can see:
//
// 🔴 A ZERO SESSION IS NOT ONE OF THEM, AND MUST NOT BECOME ONE. A row asserted by a
// producer that sent no session id holds zero, and a demotion applies against the
// session on file — so zero releases zero. Skipping such rows would make them the one
// population this door can never reach: permanently frozen, by the mechanism built to
// unfreeze them, and reported as "skipped" rather than as broken.
//
//   - The row holds no presence time. presence.acceptsDemotion requires a prior time
//     for the demotion's stamp to be newer than; without one the claim is dropped by
//     the ordering guard, indistinguishably from a stale echo.
//   - The row's presence time is not in the past. A demotion applies only when its
//     stamp is strictly AFTER the stored one, so a row stamped by a producer whose
//     clock leads ours refuses every demotion we can mint until our clock catches up.
//     This is counted rather than worked around: minting a stamp derived from the row
//     would let the operator door step over the ordering guard that every other
//     producer is bound by.
//
// The whole call is logged once at WARN with the actor and their stated reason. A
// fleet-wide presence write leaves a record whether or not anyone is watching the
// stream it writes to.
func (api *Api) DemoteAssertedPresence(ctx context.Context, source string, tokens *[]string,
	afterId uint64, limit int, actor, reason string) (*PresenceDemotionResult, error) {
	if api.demotions == nil {
		return nil, ErrNoDemotionEmitter
	}
	if strings.TrimSpace(source) == "" {
		return nil, errors.New("source must name the event source whose custody is being released")
	}
	// Refused rather than defaulted. An empty reason would satisfy String! perfectly
	// and leave the only record of a fleet-wide write saying nothing at all.
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("reason must say why this source's devices are being demoted; it is the only record of the change")
	}

	rows, err := api.AssertedStatesForDemotion(ctx, source, tokens, afterId, limit)
	if err != nil {
		return nil, err
	}

	full := demotionReason(actor, reason)
	now := api.demotions.now().UTC()
	result := &PresenceDemotionResult{}
	for _, row := range rows {
		result.Scanned++
		result.LastId = uint64(row.ID)
		if skip := demotionSkipReason(row, now); skip != "" {
			result.Skipped++
			log.Warn().Str("source", source).Str("device", row.DeviceToken).
				Str("actor", actor).Str("skipped", skip).
				Msg("Presence demotion skipped a row it could not release")
			continue
		}
		if err := api.demotions.EmitDemotion(ctx, source, row.DeviceToken, row.SessionId, now, full); err != nil {
			// The page is abandoned rather than partially reported. A count that
			// included rows whose write failed would tell the caller they were
			// demoted, and the caller's next page starts after them — so the failure
			// would be invisible AND unrepeated. Returning the error leaves the cursor
			// where it was, and the identical call is idempotent at the dedup window.
			return nil, fmt.Errorf("%w: device %s: %w", ErrDemotionPublish, row.DeviceToken, err)
		}
		result.Demoted++
	}

	log.Warn().Str("source", source).Str("actor", actor).Str("reason", reason).
		Int32("scanned", result.Scanned).Int32("demoted", result.Demoted).
		Int32("skipped", result.Skipped).Uint64("afterId", afterId).
		Msg("Operator demoted a presence source's asserted device states")
	return result, nil
}

// demotionSkipReason names why a row cannot be demoted, or "" when it can. The two
// conditions are documented on DemoteAssertedPresence; they live in one function so the
// count, the log line and the reasoning cannot drift apart.
//
// The first is a FAIL-SAFE rather than a live case: every write path stamps
// PresenceSource and PresenceTime together, so an asserted row without a presence time
// is a shape this code does not produce. It is kept because the cost of being wrong
// about that is an event the ordering guard drops in silence.
func demotionSkipReason(row *DeviceState, now time.Time) string {
	if !row.PresenceTime.Valid {
		return "the row holds no presence time, so the ordering guard has nothing to judge the demotion against"
	}
	if !now.After(row.PresenceTime.Time) {
		return "the row's presence time is not in the past, so a demotion stamped now would be refused as stale"
	}
	return ""
}
