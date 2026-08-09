// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"io"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/geofence"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// signalFenceSet marshals one FROZEN fence set onto the single-writer loop, where applyFenceSet
// installs it in the loop-owned projection (ADR-078).
//
// 🔴 IT CARRIES THE VALUE, WHERE THE ROSTER AND ATTRIBUTE SIGNALS CARRY ONLY AN IDENTITY. Those
// two send a RECHECK because the thing they mirror is mutable: two consumers over reorderable
// streams could otherwise install a stale observation, so the loop re-reads the converged
// projection instead. A fence-set VERSION has no such hazard — version 7's snapshot is frozen at
// mint and nothing ever rewrites it — so there is no order in which two of these facts could
// arrive that makes either wrong, and Put is keyed on the version each one carries. Sending the
// value is therefore not a shortcut past the convergence discipline; it is what that discipline
// reduces to when the mirrored state is immutable.
//
// It is a no-op when the projection is disabled (nil fenceView, the scaffold path — the view is
// built only when a fence source is wired). Returns false ONLY on shutdown mid-send, in which case
// the caller leaves the fact unacked; there is no durable projection to fall back on here, so the
// fact redelivers next start and, failing that, the startup reconcile re-seeds the current version
// from device-management's archive.
func (rp *ResolvedEventsProcessor) signalFenceSet(tenant string, set *geofence.FenceSet) bool {
	if rp.fenceView == nil {
		return true
	}
	select {
	case rp.fenceUpdates <- fenceUpdate{tenant: tenant, set: set}:
		return true
	case <-rp.procCtx.Done():
		return false
	}
}

// runFenceSetConsumer drains device-management's geofence-set fact stream (ADR-078): the FROZEN
// fence set of each newly-minted fence-set version. For each fact it hands the compiled set to the
// single-writer loop and then acks.
//
// It is the ONLY fact consumer here with no persist-before-ack step, and the asymmetry is
// deliberate rather than an omission. The other consumers ack against a durable projection they
// own because the fact stream's finite retention is not a system of record. For fence sets the
// system of record already exists — device-management stores every version's snapshot durably —
// so building a second copy here would be a duplicate archive to keep honest, and the restart
// path reads the original instead (reconcileFenceView). What survives a restart is therefore
// device-management's row, not a row of ours.
func (rp *ResolvedEventsProcessor) runFenceSetConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.FenceSetReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.FenceSetReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		if !rp.handleFenceSetFact(msg) {
			return // shutdown mid-send: leave unacked; it redelivers next start
		}
	}
}

// handleFenceSetFact compiles one fence-set fact, installs it on the single-writer loop, and acks.
//
// It takes no "signal" flag, unlike the roster/entity-deleted handlers, because there is no
// pre-replay catch-up drain for this stream and there could not usefully be one: those drains exist
// to bring a DURABLE PROJECTION current before the gate reads it, and this consumer maintains no
// durable projection — the startup reconcile reads device-management's archive instead, which is
// strictly fresher than any backlog the stream still holds.
//
// It returns false ONLY on a shutdown mid-send (the fact is left unacked to redeliver); a poison or
// malformed fact is dropped-and-acked and returns true.
func (rp *ResolvedEventsProcessor) handleFenceSetFact(msg messaging.Message) bool {
	tenant, ev, ok := decodeFenceSetFact(rp, msg)
	if !ok {
		// Unparseable/poison: redelivery cannot fix it. Ack so it stops redelivering forever.
		rp.ackFact(msg, "geofence-set")
		return true
	}
	// A non-positive version is unusable: 0 is the reserved "the tenant had never created a
	// fence" stamp, which the view answers from knowledge without consulting the map, so a fact
	// claiming to BE version 0 could only overwrite that with something a producer invented.
	// Nothing legitimate mints one — mintGeoFenceSetVersion starts at 1 — so this is a forged or
	// buggy-producer fact, dropped-and-acked rather than filed.
	if ev.Version <= 0 {
		log.Warn().Int32("version", ev.Version).Str("subject", msg.Subject).
			Msg("Dropping malformed geofence-set fact (non-positive fence-set version).")
		rp.ackFact(msg, "geofence-set")
		return true
	}
	fences := make([]geofence.SnapshotFence, 0, len(ev.Fences))
	for _, f := range ev.Fences {
		fences = append(fences, geofence.SnapshotFence{Token: f.Token, Geometry: f.Geometry})
	}
	// Compiled on the CONSUMER goroutine, not on the loop: compiling a hundred fences of a few
	// hundred vertices each is real work, and the loop is the one goroutine every tenant's event
	// processing shares. What crosses to the loop is the finished, immutable set. A fence whose
	// geometry will not compile is retained with its error rather than dropped, so one bad fence
	// cannot disable containment for the rest of the tenant's set.
	if !rp.signalFenceSet(tenant, geofence.NewFenceSet(ev.Version, fences)) {
		return false // shutdown mid-send: leave unacked; it redelivers next start
	}
	rp.ackFact(msg, "geofence-set")
	return true
}

// decodeFenceSetFact unmarshals one geofence-set fact into its owning tenant (from the per-tenant
// subject) and payload, with the same drop-not-fatal contract as decodeRosterFact. The tenant
// travels on the subject, never the payload — which is what makes one tenant's fences unreachable
// through another tenant's projection entry however the payload is shaped.
func decodeFenceSetFact(rp *ResolvedEventsProcessor, msg messaging.Message) (string, *dmmodel.GeoFenceSetMintedEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping geofence-set fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmmodel.UnmarshalGeoFenceSetMintedEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping geofence-set fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}
