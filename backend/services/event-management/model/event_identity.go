// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
)

// EventIdSize is the width of a derived event identity in bytes (SHA-256).
//
// The full digest is kept rather than a truncation. 32 bytes in the primary key of the
// largest hypertable is not free, and a 128-bit prefix would be statistically ample for
// this domain — but a truncation invites a recurring "why is it short?" review, and the
// storage question is a pre-GA change if it ever becomes a real cost. An accidental
// collision here is silent data loss, which is the failure this whole change exists to
// remove, so the conservative width is the right default.
const EventIdSize = sha256.Size

// DeriveEventId computes a base event's identity from its own content.
//
// 🔴 WHY CONTENT-DERIVED, and not a minted id. The identity has to satisfy two things at
// once: it must be UNIQUE per distinct event, and it must be IDENTICAL across a redelivery
// of the same event. A random id minted anywhere in the pipeline satisfies the first and
// fails the second — a JetStream redelivery replays the raw publish, so an id generated
// after that point differs on every attempt and dedup collapses. Making a minted id stable
// means stamping it before the durable capture, which the MQTT gateway could do (captureSeq
// is already threaded through) but lwm2m-ingest and sparkplug-ingest cannot: neither has a
// capture stream. Deriving from content needs no coordination and no threading, so it holds
// on every transport, including ones not written yet.
//
// The trade it makes: two byte-identical readings from one device at one instant collapse
// to a single event. That is the correct reading of indistinguishable inputs — nothing
// downstream could tell them apart either — and it is why alt_id is keyed, so a device that
// wants two such readings kept apart says so with its own idempotency key.
//
// Fields are length-prefixed, never concatenated. Plain concatenation lets ("ab","c") and
// ("a","bc") hash identically, which would let a device token ending in the delimiter carry
// another device's identity — a forgeable key, not merely an ambiguous one.
//
// occurred_time is keyed at NANOSECOND resolution deliberately. Hashing a truncated time
// would reintroduce the exact collision this change removes, one layer further in and much
// harder to see.
func DeriveEventId(tenantId string, event *Event, canonicalPayload []byte) []byte {
	h := sha256.New()
	writeField(h, []byte(tenantId))
	writeField(h, []byte(event.DeviceToken))

	var eventType [8]byte
	binary.BigEndian.PutUint64(eventType[:], uint64(event.EventType))
	writeField(h, eventType[:])

	var occurred [8]byte
	binary.BigEndian.PutUint64(occurred[:], uint64(event.OccurredTime.UTC().UnixNano()))
	writeField(h, occurred[:])

	// An absent alternateId is distinguished from an empty one: a present-but-empty id is
	// a claim the device made, absence is not, and collapsing them would let one stand in
	// for the other.
	if event.AltId.Valid {
		writeField(h, []byte{1})
		writeField(h, []byte(event.AltId.String))
	} else {
		writeField(h, []byte{0})
	}

	writeField(h, canonicalPayload)
	return h.Sum(nil)
}

// DerivePayloadId computes one payload row's identity from its parent event and its own
// content, so a redelivery converges on the same row.
//
// 🔴 WHY PAYLOAD ROWS NEED THEIR OWN IDENTITY AT ALL. Giving the base event one is not
// enough: PersistEvent's dedup probe is keyed on alt_id, and lwm2m-ingest and
// sparkplug-ingest set no alt_id anywhere, so for those two transports the probe never
// engages. The base event still converged on its content digest, but payload rows inserted
// unconditionally — leaving one envelope owning N copies of its own measurements after N
// deliveries, on a path (at-least-once redelivery) the system is designed around.
//
// state_change_events already carried exactly this fix, and only for itself, for exactly
// this reason ("a StateChange carries no AltId"). This generalises it.
//
// 🔴 WHY A CONTENT DIGEST RATHER THAN A POSITION OR A NATURAL COLUMN — both cheaper
// options are wrong, and each is wrong in its own way:
//
//   - An ORDINAL (the entry's index within the message) is not stable across a redelivery.
//     UnresolvedMeasurementsEntry.Measurements is a map[string]string, and Go randomises
//     map iteration, so the same bytes decode to the same set in a different ORDER every
//     time. An ordinal key would therefore admit duplicates under different ordinals —
//     failing exactly where it is needed while looking like it works.
//   - A NATURAL COLUMN does not discriminate. (event_id, name) suits measurements, but the
//     equivalent (event_id, type) for alerts silently eats a second SENSOR_FAULT carrying a
//     different message — turning a duplicate-row bug into a lost-row bug, which is worse.
//
// Hashing the entry's canonical content is uniform across all three payload tables, immune
// to map ordering, and collapses only entries that are byte-identical — which nothing
// downstream could have told apart anyway.
//
// Scoped to the parent event so the same reading under two different events stays distinct.
func DerivePayloadId(eventId []byte, canonicalEntry []byte) []byte {
	h := sha256.New()
	writeField(h, eventId)
	writeField(h, canonicalEntry)
	return h.Sum(nil)
}

// writeField feeds one length-prefixed field into the digest, so field boundaries are
// unambiguous regardless of the bytes inside them.
func writeField(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write(value)
}
