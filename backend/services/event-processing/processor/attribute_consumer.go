// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"io"
	"math"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// attributeFactPoison validates a decoded device-attribute fact against the projection's invariants,
// returning a short reason and true when the fact must be dropped-and-acked rather than persisted.
// Every check defends a real hazard from a FORGED or buggy-producer fact (the proto imposes no
// constraints), because persisting one would either wedge the persist-before-ack retry on a
// permanent error or seed a durable value the eval path (slice 4c-3b-2) would silently mis-handle:
//
//   - device/key/scope empty or over-length: the (device, scope, key) tuple is the row identity; an
//     empty component is unusable and an over-length one overflows the varchar column (a permanent
//     insert error → wedge). Device and key are ADR-042 tokens (<= core.MaxTokenLen); a threshold
//     key beyond that bound simply cannot be a dynamic-threshold source, so it is dropped here (note
//     device-management does not itself grammar-bound an attribute key today — surfacing that to the
//     author is a slice 4c-3b-2 concern where the rule-side threshold-key grammar is defined).
//   - scope not in the platform-set vocabulary (SHARED/SERVER): CLIENT is excluded upstream so a
//     device cannot set the limit it is checked against; whitelisting here is defense-in-depth so a
//     forged CLIENT/junk-scope fact cannot seed a live row the flattening (4c-3b-2) would weigh.
//   - a non-finite Value on a SET (NaN/±Inf): the producer filters these (emitting a removal
//     instead), so a set carrying one is forged; persisting it would poison a CEL comparison (always
//     false against NaN), silently disabling the rule for that device.
//   - a zero UpdatedAt: it would defeat the monotonic guard and the deletion fence.
func attributeFactPoison(ev *dmmodel.DeviceAttributeEvent) (string, bool) {
	switch {
	case ev.DeviceToken == "" || !validRosterToken(ev.DeviceToken):
		return "empty or over-long device token", true
	case ev.AttrKey == "" || !validRosterToken(ev.AttrKey):
		return "empty or over-long attribute key", true
	case !validAttributeScope(ev.Scope):
		return "scope not in the SHARED/SERVER vocabulary", true
	case !ev.Removed && (math.IsNaN(ev.Value) || math.IsInf(ev.Value, 0)):
		return "non-finite value on a set", true
	case ev.UpdatedAt.IsZero():
		return "zero updated-at", true
	}
	return "", false
}

// validAttributeScope reports whether a fact's scope is one of the two platform-set scopes the
// dynamic-threshold projection tracks. CLIENT and any forged/junk value are rejected.
func validAttributeScope(scope string) bool {
	return scope == string(dmmodel.AttributeScopeShared) || scope == string(dmmodel.AttributeScopeServer)
}

// runAttributeConsumer drains device-management's device-attribute fact stream (ADR-051 slice 4c-3):
// the numeric, platform-set attributes a detection rule can read a DYNAMIC threshold from. For each
// fact it persists the value (or a removal tombstone) to the durable DeviceAttribute projection
// BEFORE acking, so the value survives a restart independent of the finite-retention stream — the
// same persist-before-ack spine the rule/roster consumers use. This slice only lands the projection;
// the engine eval that reads it (the CEL "attr" var, slice 4c-3b-2) is not wired here, so nothing is
// marshaled onto the single-writer loop — the consumer is engine-inert.
func (rp *ResolvedEventsProcessor) runAttributeConsumer() {
	defer rp.readerWG.Done()
	for {
		msg, err := rp.AttributeReader.ReadMessage(rp.procCtx)
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			rp.AttributeReader.HandleResponse(err)
			select {
			case <-time.After(readErrorBackoff):
			case <-rp.procCtx.Done():
				return
			}
			continue
		}
		tenant, ev, ok := decodeAttributeFact(rp, msg)
		if !ok {
			// Unparseable/poison: redelivery cannot fix it. Ack so it stops redelivering forever.
			rp.ackFact(msg, "device-attribute")
			continue
		}
		if reason, bad := attributeFactPoison(ev); bad {
			// A malformed/forged fact: dropping-and-acking rather than persisting it or wedging the
			// persist-before-ack retry on a permanent error. Redelivery cannot fix any of these.
			log.Warn().Str("device", ev.DeviceToken).Str("scope", ev.Scope).Str("key", ev.AttrKey).
				Str("reason", reason).Str("subject", msg.Subject).Msg("Dropping malformed device-attribute fact.")
			rp.ackFact(msg, "device-attribute")
			continue
		}
		if rp.AttributeStore != nil {
			desc := "device-attribute " + tenant + "/" + ev.DeviceToken + "/" + ev.Scope + "/" + ev.AttrKey
			var op func() error
			if ev.Removed {
				op = func() error {
					return rp.AttributeStore.Remove(rp.procCtx, tenant, ev.DeviceToken, ev.Scope, ev.AttrKey, ev.UpdatedAt)
				}
			} else {
				attr := &model.DeviceAttribute{
					Tenant:      tenant,
					DeviceToken: ev.DeviceToken,
					Scope:       ev.Scope,
					AttrKey:     ev.AttrKey,
					Value:       ev.Value,
					LastEventAt: ev.UpdatedAt,
				}
				op = func() error { return rp.AttributeStore.Upsert(rp.procCtx, attr) }
			}
			if !rp.persistBeforeAck(desc, op) {
				return // shutdown mid-retry: leave unacked; the fact redelivers next start
			}
		}
		rp.ackFact(msg, "device-attribute")
	}
}

// decodeAttributeFact unmarshals one device-attribute fact into its owning tenant (from the
// per-tenant subject) and payload, with the same drop-not-fatal contract as decodeRosterFact. The
// tenant travels on the subject, never the payload.
func decodeAttributeFact(rp *ResolvedEventsProcessor, msg messaging.Message) (string, *dmmodel.DeviceAttributeEvent, bool) {
	_, tenant, ok := messaging.TenantContextFromSubject(rp.procCtx, msg.Subject)
	if !ok {
		log.Warn().Str("correlation", msg.CorrelationID()).
			Msgf("Dropping device-attribute fact with no parseable tenant in subject %q", msg.Subject)
		return "", nil, false
	}
	ev, err := dmproto.UnmarshalDeviceAttributeEvent(msg.Value)
	if err != nil {
		log.Warn().Err(err).Str("correlation", msg.CorrelationID()).
			Msgf("Dropping device-attribute fact that could not be parsed from subject %q", msg.Subject)
		return "", nil, false
	}
	return tenant, ev, true
}
