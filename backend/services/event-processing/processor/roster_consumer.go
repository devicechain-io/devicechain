// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"io"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// signalArmRecheck marshals a device-membership RECHECK onto the single-writer loop for the dead-man
// armer (ADR-051 slice 4c-2b-2b). It sends only the device identity — the loop re-reads the
// authoritative roster projection and arms/disarms from that (applyArmRecheck). Deferring the read to
// the loop is deliberate: it is what keeps the roster and entity-deleted consumers (separate
// goroutines over reorderable streams) from applying their observations out of lifecycle order, since
// each consumer's own read+send is not atomic. It is a no-op when arming is disabled (nil armer, the
// scaffold path; the armer is built only when the read-model stores are wired). Returns false ONLY on
// shutdown mid-send (the caller then leaves the fact unacked; the durable projection + restart
// reconcile rebuild arming). The persist already committed the row, so a recheck is never lost to a
// dropped in-memory signal — at worst it is re-derived by reconcile.
func (rp *ResolvedEventsProcessor) signalArmRecheck(tenant, deviceToken string) bool {
	if rp.armer == nil {
		return true
	}
	select {
	case rp.armUpdates <- armUpdate{tenant: tenant, deviceToken: deviceToken}:
		return true
	case <-rp.procCtx.Done():
		return false
	}
}

// validRosterToken bounds a token decoded from a fact before it becomes a durable projection
// column. A well-formed producer only ever emits ADR-042 tokens (<= core.MaxTokenLen), but the
// proto imposes no length, so a forged or buggy-producer fact could carry an over-long token that
// overflows the varchar(256) column on postgres — a PERMANENT insert error that would wedge the
// persist-before-ack retry forever and head-of-line-block the whole projection. Bounds-checking at
// consume turns that poison into a drop-and-ack, mirroring the decode-poison path. Empty is allowed
// for a profile token (a device whose type has no profile) but not where noted (a device token).
func validRosterToken(tok string) bool { return len(tok) <= core.MaxTokenLen }

// persistBeforeAck retries op until it commits or the loop shuts down, capping the backoff at
// maxRulePersistBackoff. It is the durability primitive the fact consumers share (ADR-051): the
// durable projection — not the finite-retention fact stream — is the restart source of truth, so
// a fact must stay UNACKED until its rows are committed, defeating the broker's finite redelivery
// (MaxDeliver) if the store is briefly down. op must be idempotent (a redelivery re-runs it), and
// cross-fact ordering must be irrelevant (it is: roster keys on the device, rule facts on the
// unique version token), since this blocks the consumer while the store recovers. desc labels the
// retry log. Returns false ONLY on shutdown mid-retry — the caller then returns without acking, so
// the fact redelivers next start.
func (rp *ResolvedEventsProcessor) persistBeforeAck(desc string, op func() error) bool {
	backoff := readErrorBackoff
	for op() != nil {
		log.Error().Str("what", desc).Msg("Failed to persist a fact projection; retrying (fact stays unacked).")
		select {
		case <-time.After(backoff):
		case <-rp.procCtx.Done():
			return false
		}
		if backoff *= 2; backoff > maxRulePersistBackoff {
			backoff = maxRulePersistBackoff
		}
	}
	return true
}

// runRosterConsumer drains device-management's device-roster fact stream (ADR-051 slice 4c-2b).
// For each fact it persists the device's roster row to the durable projection BEFORE acking, so
// the set of devices expected to report survives a restart independent of the finite-retention
// stream — the same persist-before-ack durability spine the rule consumer uses. A re-typed device
// upserts its row in place (last-wins). This slice only lands the projection; the engine dead-man
// arming that reads it is slice 4c-2b-2, so nothing is marshaled onto the single-writer loop here.
func (rp *ResolvedEventsProcessor) runRosterConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.RosterReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.RosterReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		tenant, ev, ok := decodeRosterFact(rp, msg)
		if !ok {
			// Unparseable/poison: redelivery cannot fix it. Ack so it stops redelivering forever.
			rp.ackFact(msg, "device-roster")
			continue
		}
		// A device token is the row's identity and its dead-man series key, so an empty or over-long
		// one is unusable poison (a forged/buggy-producer fact): drop-and-ack rather than persist a
		// phantom row or wedge on a varchar overflow. A zero ExpectedSince would also defeat the
		// monotonic guard, so it is dropped too.
		if ev.DeviceToken == "" || !validRosterToken(ev.DeviceToken) || !validRosterToken(ev.ProfileToken) || ev.ExpectedSince.IsZero() {
			log.Warn().Str("device", ev.DeviceToken).Str("subject", msg.Subject).
				Msg("Dropping malformed device-roster fact (empty/over-long token or zero expected-since).")
			rp.ackFact(msg, "device-roster")
			continue
		}
		if rp.RosterStore != nil {
			roster := &model.DeviceRoster{
				Tenant:        tenant,
				DeviceToken:   ev.DeviceToken,
				ProfileToken:  ev.ProfileToken,
				ExpectedSince: ev.ExpectedSince,
			}
			if !rp.persistBeforeAck("device-roster "+tenant+"/"+ev.DeviceToken,
				func() error { return rp.RosterStore.Upsert(rp.procCtx, roster) }) {
				return // shutdown mid-retry: leave unacked; the row redelivers next start
			}
			if !rp.signalArmRecheck(tenant, ev.DeviceToken) {
				return // shutdown mid-send: leave unacked; reconcile rebuilds arming next start
			}
		}
		rp.ackFact(msg, "device-roster")
	}
}

// runEntityDeletedConsumer drains device-management's entity-deleted fact stream (ADR-044) as an
// independent consumer, tearing down a DELETED DEVICE's derived state: it tombstones the device's
// roster row so its dead-man arming is cancelled rather than firing a false absence forever (ADR-051
// slice 4c-2b), AND purges the device's dynamic-threshold attribute projection so a reused token
// cannot inherit its thresholds (slice 4c-3). The stream carries every edge entity's deletion; a
// non-device deletion is nothing these projections track, so it is acked and ignored. Both teardowns
// are idempotent (removing/purging absent rows is a no-op), persisted before ack.
func (rp *ResolvedEventsProcessor) runEntityDeletedConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.EntityDeletedReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.EntityDeletedReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		tenant, ev, ok := decodeEntityDeletedFact(rp, msg)
		if !ok {
			rp.ackFact(msg, "entity-deleted")
			continue
		}
		// Only a device deletion touches these projections; every other entity type (groups, assets,
		// relationships, anchors) is consumed elsewhere (event-management) and ignored here.
		// DeletedTime is the lifecycle clock both teardowns are ordered on, so a stale redelivered
		// delete cannot erase a device whose newer create/re-set already advanced the clock.
		if ev.EntityType == entity.TypeDevice && (rp.RosterStore != nil || rp.AttributeStore != nil) {
			// A malformed device fact is skipped rather than applied — symmetric with the roster
			// consumer's drop: an over-long token would overflow the tombstone/fence insert, and a
			// zero DeletedTime would defeat the monotonic guard (silently no-op'ing against a live
			// row). The well-formed producer always stamps a real token and time, so this only guards
			// a forged/buggy-producer fact.
			if ev.EntityToken == "" || !validRosterToken(ev.EntityToken) || ev.DeletedTime.IsZero() {
				log.Warn().Str("device", ev.EntityToken).Str("subject", msg.Subject).
					Msg("Dropping malformed device entity-deleted fact (empty/over-long token or zero deleted-time).")
			} else {
				deletedTime := ev.DeletedTime
				if rp.RosterStore != nil {
					if !rp.persistBeforeAck("roster-delete "+tenant+"/"+ev.EntityToken,
						func() error { return rp.RosterStore.Delete(rp.procCtx, tenant, ev.EntityToken, deletedTime) }) {
						return // shutdown mid-retry: leave unacked; the deletion redelivers next start
					}
					// Signal the loop to re-check membership: it re-reads the projection (a tombstone
					// disarms; a stale delete the monotonic guard rejected leaves the row live, so the
					// re-read arms — no spurious disarm) in lifecycle order regardless of consumer races.
					if !rp.signalArmRecheck(tenant, ev.EntityToken) {
						return // shutdown mid-send: leave unacked; reconcile rebuilds arming next start
					}
				}
				// Purge the device's dynamic-threshold attributes and drop its resurrection fence
				// (ADR-051 slice 4c-3), so a straggler set reordered after the deletion cannot leave a
				// phantom value a reused token would inherit. Idempotent and monotonic on deletedTime,
				// so a redelivered deletion is a no-op and cannot erase a token-reused device's newer
				// attributes. Engine-inert (nothing marshals onto the loop for it in this slice).
				if rp.AttributeStore != nil {
					if !rp.persistBeforeAck("attribute-purge "+tenant+"/"+ev.EntityToken,
						func() error { return rp.AttributeStore.PurgeDevice(rp.procCtx, tenant, ev.EntityToken, deletedTime) }) {
						return // shutdown mid-retry: leave unacked; the purge redelivers next start
					}
				}
			}
		}
		rp.ackFact(msg, "entity-deleted")
	}
}

// ackFact acks a best-effort fact, logging a failed ack (harmless: a redelivered fact re-persists
// idempotently). It is the shared ack for the roster/entity-deleted consumers; kind labels the log.
func (rp *ResolvedEventsProcessor) ackFact(msg messaging.Message, kind string) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Str("kind", kind).Msg("Failed to ack a fact; it will redeliver (idempotent).")
	}
}

// decodeRosterFact unmarshals one device-roster fact into its owning tenant (from the per-tenant
// subject) and payload. ok is false when the message is unusable (no parseable tenant, or an
// unmarshalable payload) — dropped, not fatal. The tenant travels on the subject, never the payload.
func decodeRosterFact(rp *ResolvedEventsProcessor, msg messaging.Message) (string, *dmmodel.DeviceRosterEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping device-roster fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmproto.UnmarshalDeviceRosterEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping device-roster fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}

// decodeEntityDeletedFact unmarshals one entity-deleted fact into its owning tenant and payload,
// with the same drop-not-fatal contract as decodeRosterFact.
func decodeEntityDeletedFact(rp *ResolvedEventsProcessor, msg messaging.Message) (string, *dmmodel.EntityDeletedEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping entity-deleted fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmproto.UnmarshalEntityDeletedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping entity-deleted fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}
